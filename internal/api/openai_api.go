package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/openclaw"
	"github.com/qcy/weclaw/pkg/logger"
)

// OpenAIAPI provides OpenAI-compatible API endpoints.
type OpenAIAPI struct {
	cfg              *config.Config
	containerService *container.Service
	containerMgr     *container.Manager
	openclawClient   *openclaw.Client
}

// NewOpenAIAPI creates a new OpenAI API handler.
func NewOpenAIAPI(
	cfg *config.Config,
	containerService *container.Service,
	containerMgr *container.Manager,
	openclawClient *openclaw.Client,
) *OpenAIAPI {
	return &OpenAIAPI{
		cfg:              cfg,
		containerService: containerService,
		containerMgr:     containerMgr,
		openclawClient:   openclawClient,
	}
}

// RegisterRoutes registers OpenAI API routes.
func (api *OpenAIAPI) RegisterRoutes(r *gin.Engine) {
	v1 := r.Group("/v1")
	{
		v1.POST("/chat/completions", api.ChatCompletions)

		v1.OPTIONS("/*path", func(c *gin.Context) {
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Container-ID")
			c.Status(http.StatusNoContent)
		})
	}
}

// ChatCompletions simulates the OpenAI Chat Completions API.
// Uses JWT auth (Bearer token) and X-Container-ID header to identify the container.
func (api *OpenAIAPI) ChatCompletions(c *gin.Context) {
	// 1. Authenticate via JWT
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "Missing Authorization header"}})
		return
	}

	// Use AuthMiddleware-style JWT parsing inline
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := parseJWTClaims(tokenString)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "Invalid or expired token"}})
		return
	}

	accountID := uint(0)
	if sub, ok := claims["sub"].(float64); ok {
		accountID = uint(sub)
	}
	if accountID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "Invalid token claims"}})
		return
	}

	// 2. Get container ID from header
	containerIDStr := c.GetHeader("X-Container-ID")
	if containerIDStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "X-Container-ID header is required"}})
		return
	}
	containerID, err := strconv.ParseUint(containerIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "Invalid X-Container-ID"}})
		return
	}

	// 3. Resolve container with ownership check
	ctr, err := api.containerService.GetByID(uint(containerID), accountID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"message": "Container not found"}})
		return
	}

	if ctr.ContainerID == "" || ctr.ContainerPort == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "Container has no Docker instance. Status: " + ctr.Status}})
		return
	}

	// 4. Parse request body
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "Invalid request body"}})
		return
	}

	// 5. Extract the last user message
	var lastPrompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastPrompt = req.Messages[i].Content
			break
		}
	}

	if lastPrompt == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "No user message found to process"}})
		return
	}

	// 6. Wake up if sleeping
	if err := api.containerService.EnsureRunning(ctr); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "Failed to wake up container"}})
		return
	}

	// 7. Send message to OpenClaw
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	response, err := api.openclawClient.SendMessage(ctx, ctr.ContainerPort, ctr.GatewayToken, lastPrompt)
	if err != nil {
		logger.Error("OpenAI API OpenClaw Send Error", "container_id", ctr.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": fmt.Sprintf("OpenClaw error: %v", err)}})
		return
	}

	// Log and update activity
	_ = api.containerService.LogMessage(ctr.ID, "incoming", "openai_api", lastPrompt)
	_ = api.containerService.LogMessage(ctr.ID, "outgoing", "openai_api", response)
	_ = api.containerService.TouchActivity(ctr.ID)

	responseID := fmt.Sprintf("chatcmpl-%s", time.Now().Format("20060102150405"))
	modelName := req.Model
	if modelName == "" {
		modelName = "weclaw-openclaw"
	}

	// 8. Return response
	if req.Stream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")

		chunk := gin.H{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   modelName,
			"choices": []gin.H{
				{
					"index":         0,
					"delta":         gin.H{"content": response},
					"finish_reason": nil,
				},
			},
		}

		endChunk := gin.H{
			"id":      responseID,
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   modelName,
			"choices": []gin.H{
				{
					"index":         0,
					"delta":         gin.H{},
					"finish_reason": "stop",
				},
			},
		}

		chunkBytes, _ := json.Marshal(chunk)
		c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", string(chunkBytes))))
		c.Writer.Flush()

		endChunkBytes, _ := json.Marshal(endChunk)
		c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", string(endChunkBytes))))
		c.Writer.Write([]byte("data: [DONE]\n\n"))
		c.Writer.Flush()
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      responseID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []gin.H{
			{
				"index":         0,
				"message":       gin.H{"role": "assistant", "content": response},
				"finish_reason": "stop",
			},
		},
		"usage": gin.H{
			"prompt_tokens":     strings.Count(lastPrompt, "") - 1,
			"completion_tokens": strings.Count(response, "") - 1,
			"total_tokens":      (strings.Count(lastPrompt, "") - 1) + (strings.Count(response, "") - 1),
		},
	})
}

// parseJWTClaims parses and validates a JWT token string, returning claims.
func parseJWTClaims(tokenString string) (map[string]interface{}, error) {
	token, err := parseJWT(tokenString)
	if err != nil {
		return nil, err
	}
	return token, nil
}
