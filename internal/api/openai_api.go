package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/openclaw"
	"github.com/qcy/weclaw/internal/user"
	"github.com/qcy/weclaw/pkg/logger"
)

// OpenAIAPI provides OpenAI-compatible API endpoints.
type OpenAIAPI struct {
	cfg            *config.Config
	userService    *user.Service
	containerMgr   *container.Manager
	openclawClient *openclaw.Client
}

// NewOpenAIAPI creates a new OpenAI API handler.
func NewOpenAIAPI(
	cfg *config.Config,
	userService *user.Service,
	containerMgr *container.Manager,
	openclawClient *openclaw.Client,
) *OpenAIAPI {
	return &OpenAIAPI{
		cfg:            cfg,
		userService:    userService,
		containerMgr:   containerMgr,
		openclawClient: openclawClient,
	}
}

// RegisterRoutes registers OpenAI API routes.
func (api *OpenAIAPI) RegisterRoutes(r *gin.Engine) {
	v1 := r.Group("/v1")
	{
		// Supports /v1/chat/completions logic
		v1.POST("/chat/completions", api.ChatCompletions)

		// For CORS preflight specifically for /v1 (as some clients are strict)
		v1.OPTIONS("/*path", func(c *gin.Context) {
			c.Header("Access-Control-Allow-Origin", "*")
			c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type")
			c.Status(http.StatusNoContent)
		})
	}
}

// ChatCompletions simulates the OpenAI Chat Completions API.
// It uses the Authorization header (Bearer token) as the user OpenID.
// For example: Authorization: Bearer {openid}
func (api *OpenAIAPI) ChatCompletions(c *gin.Context) {
	// 1. Get OpenID from Auth header
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "Missing Authorization header with token (OpenID)"}})
		return
	}
	openID := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))

	// Parse input request body
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

	// 2. Find User and specific state
	u, err := api.userService.FindByOpenID(openID)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "User not found. Please register first via WeChat or test APIs"}})
		return
	}

	if u.ContainerID == "" || u.ContainerPort == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "User has no container attached. User status: " + string(u.Status)}})
		return
	}

	// 3. Extract the last prompt sent by User
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

	// 4. Check quota and status
	allowed, err := api.userService.CheckQuota(openID)
	if err != nil {
		logger.Error("Failed to check quota", "openid", openID, "error", err)
	}
	if !allowed {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": gin.H{"message": "Quota exceeded for today"}})
		return
	}

	if u.Status == user.StatusSleeping {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		if err := api.containerMgr.StartContainer(ctx, u.ContainerID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "Failed to wake up container"}})
			return
		}
		_ = api.userService.UpdateStatus(openID, user.StatusActive)
		time.Sleep(3 * time.Second)
	}

	// 5. Send message to OpenClaw
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	response, err := api.openclawClient.SendMessage(ctx, u.ContainerName, lastPrompt)
	if err != nil {
		logger.Error("OpenAI API OpenClaw Send Error", "openid", openID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": fmt.Sprintf("OpenClaw error: %v", err)}})
		return
	}

	// Logging and updating activity
	_ = api.userService.LogMessage(u.ID, "incoming", "openai_api", lastPrompt)
	_ = api.userService.LogMessage(u.ID, "outgoing", "openai_api", response)
	_ = api.userService.IncrementMsgCount(openID)
	_ = api.userService.TouchActivity(openID)

	responseID := fmt.Sprintf("chatcmpl-%s", time.Now().Format("20060102150405"))
	modelName := req.Model
	if modelName == "" {
		modelName = "weclaw-openclaw" // fallback
	}

	// 6. Return response
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
