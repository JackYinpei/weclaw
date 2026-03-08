package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/openclaw"
	"github.com/qcy/weclaw/internal/user"
	"github.com/qcy/weclaw/pkg/logger"
)

// TestAPI provides test/debug API endpoints.
type TestAPI struct {
	cfg            *config.Config
	userService    *user.Service
	containerMgr   *container.Manager
	openclawClient *openclaw.Client
}

// NewTestAPI creates a new test API handler.
func NewTestAPI(
	cfg *config.Config,
	userService *user.Service,
	containerMgr *container.Manager,
	openclawClient *openclaw.Client,
) *TestAPI {
	return &TestAPI{
		cfg:            cfg,
		userService:    userService,
		containerMgr:   containerMgr,
		openclawClient: openclawClient,
	}
}

// RegisterRoutes registers test API routes.
func (t *TestAPI) RegisterRoutes(r *gin.Engine) {
	testGroup := r.Group("/api/test")
	testGroup.Use(AuthMiddleware()) // Protect test API routes
	{
		testGroup.GET("/docker", t.TestDocker)
		testGroup.POST("/register", t.TestRegister)
		testGroup.POST("/send", t.TestSendMessage)
		testGroup.GET("/user/:openid", t.GetUser)
		testGroup.GET("/user/:openid/messages", t.GetMessages)
		testGroup.GET("/users", t.ListUsers)
		testGroup.DELETE("/user/:openid", t.DeleteUser)
	}
}

// TestDocker tests Docker connectivity and ability to create/run containers.
// GET /api/test/docker
func (t *TestAPI) TestDocker(c *gin.Context) {
	results := make(map[string]interface{})
	results["timestamp"] = time.Now().Format(time.RFC3339)

	// Step 1: Check Docker daemon connectivity (already verified at startup)
	results["docker_connected"] = true

	// Step 2: Try to pull/check the OpenClaw image
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	imageName := t.cfg.Docker.OpenClawImage

	// Step 3: Create a test container
	testOpenID := fmt.Sprintf("test-docker-%d", time.Now().UnixMilli())
	logger.Info("Testing Docker: creating test container", "test_id", testOpenID)

	info, err := t.containerMgr.CreateContainer(ctx, testOpenID, &t.cfg.OpenClaw, nil)
	if err != nil {
		results["container_created"] = false
		results["container_error"] = err.Error()
		results["image"] = imageName
		results["status"] = "FAIL"
		c.JSON(http.StatusOK, results)
		return
	}

	results["container_created"] = true
	results["container_id"] = info.ContainerID[:12]
	results["container_name"] = info.ContainerName
	results["container_port"] = info.Port
	results["image"] = imageName

	// Step 4: Check that container is running
	running, err := t.containerMgr.IsContainerRunning(ctx, info.ContainerID)
	if err != nil {
		results["container_running"] = false
		results["check_error"] = err.Error()
	} else {
		results["container_running"] = running
	}

	// Step 5: Wait for OpenClaw Gateway to become ready (healthz check)
	results["openclaw_ready"] = false
	if running {
		logger.Info("Waiting for OpenClaw Gateway to become healthy...", "port", info.Port)

		// Poll /healthz with retries (gateway needs time to start)
		healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", info.Port)
		httpClient := &http.Client{Timeout: 5 * time.Second}

		for i := 0; i < 12; i++ { // 12 * 5s = 60s max wait
			time.Sleep(5 * time.Second)
			resp, err := httpClient.Get(healthURL)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					results["openclaw_ready"] = true
					results["openclaw_healthz"] = "OK"
					logger.Info("OpenClaw Gateway is healthy", "port", info.Port)
					break
				}
			}
			logger.Debug("OpenClaw not ready yet, retrying...", "attempt", i+1)
		}

		if results["openclaw_ready"] != true {
			results["openclaw_error"] = "Gateway did not become healthy within 60s"
		}
	}

	// Step 6: Clean up test container
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cleanupCancel()

	if err := t.containerMgr.RemoveContainer(cleanupCtx, info.ContainerID, info.Port); err != nil {
		results["cleanup_error"] = err.Error()
	} else {
		results["cleanup"] = "ok"
	}

	// Overall status
	if results["container_created"] == true && results["container_running"] == true {
		results["status"] = "OK"
	} else {
		results["status"] = "FAIL"
	}

	c.JSON(http.StatusOK, results)
}

// RegisterRequest is the request body for test registration.
type RegisterRequest struct {
	OpenID string `json:"openid" binding:"required"`
}

// TestRegister simulates a WeChat user subscription (user registration + container creation).
// POST /api/test/register  {"openid": "test-user-001"}
func (t *TestAPI) TestRegister(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "openid is required", "detail": err.Error()})
		return
	}

	openID := req.OpenID
	results := map[string]interface{}{
		"openid":    openID,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	// Check if user already exists
	existingUser, err := t.userService.FindByOpenID(openID)
	if err == nil && existingUser != nil {
		results["status"] = "already_exists"
		results["user"] = existingUser
		c.JSON(http.StatusOK, results)
		return
	}

	// Step 1: Create user
	u, err := t.userService.Create(openID)
	if err != nil {
		results["status"] = "FAIL"
		results["error"] = fmt.Sprintf("failed to create user: %v", err)
		c.JSON(http.StatusInternalServerError, results)
		return
	}
	results["user_created"] = true
	results["user_id"] = u.ID

	// Step 2: Create container (synchronously for testing)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	logger.Info("Test register: creating container", "openid", openID)

	info, err := t.containerMgr.CreateContainer(ctx, openID, &t.cfg.OpenClaw, nil)
	if err != nil {
		results["status"] = "PARTIAL"
		results["container_error"] = err.Error()
		results["message"] = "User created but container creation failed"
		c.JSON(http.StatusOK, results)
		return
	}

	results["container_created"] = true
	results["container_id"] = info.ContainerID[:12]
	results["container_name"] = info.ContainerName
	results["container_port"] = info.Port

	// Step 3: Update user with container info
	if err := t.userService.UpdateContainerInfo(
		openID,
		info.ContainerID,
		info.ContainerName,
		info.Port,
		info.GatewayToken,
	); err != nil {
		results["status"] = "PARTIAL"
		results["update_error"] = err.Error()
		c.JSON(http.StatusOK, results)
		return
	}

	results["status"] = "OK"
	results["message"] = "User registered and container created successfully"

	c.JSON(http.StatusOK, results)
}

// SendMessageRequest is the request body for sending a test message.
type SendMessageRequest struct {
	OpenID  string `json:"openid" binding:"required"`
	Message string `json:"message" binding:"required"`
}

// TestSendMessage sends a message to a user's OpenClaw container and returns the response.
// POST /api/test/send  {"openid": "test-user-001", "message": "hello"}
func (t *TestAPI) TestSendMessage(c *gin.Context) {
	var req SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "openid and message are required", "detail": err.Error()})
		return
	}

	results := map[string]interface{}{
		"openid":    req.OpenID,
		"message":   req.Message,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	// Find user
	u, err := t.userService.FindByOpenID(req.OpenID)
	if err != nil {
		results["status"] = "FAIL"
		results["error"] = "User not found. Please register first via POST /api/test/register"
		c.JSON(http.StatusNotFound, results)
		return
	}

	if u.ContainerID == "" || u.ContainerPort == 0 {
		results["status"] = "FAIL"
		results["error"] = "User has no container. User status: " + string(u.Status)
		c.JSON(http.StatusBadRequest, results)
		return
	}

	// Check if container is running, wake up if sleeping
	if u.Status == user.StatusSleeping {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()

		if err := t.containerMgr.StartContainer(ctx, u.ContainerID); err != nil {
			results["status"] = "FAIL"
			results["error"] = fmt.Sprintf("Failed to wake up container: %v", err)
			c.JSON(http.StatusInternalServerError, results)
			return
		}
		_ = t.userService.UpdateStatus(req.OpenID, user.StatusActive)
		time.Sleep(3 * time.Second) // Give container time to start
	}

	// Send message to OpenClaw
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()

	startTime := time.Now()
	response, err := t.openclawClient.SendMessage(ctx, u.ContainerName, req.Message)
	elapsed := time.Since(startTime)

	if err != nil {
		results["status"] = "FAIL"
		results["error"] = fmt.Sprintf("OpenClaw error: %v", err)
		results["elapsed"] = elapsed.String()
		c.JSON(http.StatusOK, results)
		return
	}

	// Format for WeChat
	formatted := openclaw.FormatForWeChat(response)

	results["status"] = "OK"
	results["raw_response"] = response
	results["wechat_formatted"] = formatted
	results["elapsed"] = elapsed.String()
	results["container_port"] = u.ContainerPort

	// Log message
	_ = t.userService.LogMessage(u.ID, "incoming", "text", req.Message)
	_ = t.userService.LogMessage(u.ID, "outgoing", "text", response)
	_ = t.userService.IncrementMsgCount(req.OpenID)
	_ = t.userService.TouchActivity(req.OpenID)

	c.JSON(http.StatusOK, results)
}

// GetUser gets user information by OpenID.
// GET /api/test/user/:openid
func (t *TestAPI) GetUser(c *gin.Context) {
	openID := c.Param("openid")

	u, err := t.userService.FindByOpenID(openID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found", "openid": openID})
		return
	}

	// Also check if container is running
	containerRunning := false
	if u.ContainerID != "" {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		running, err := t.containerMgr.IsContainerRunning(ctx, u.ContainerID)
		if err == nil {
			containerRunning = running
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"user":              u,
		"container_running": containerRunning,
	})
}

// GetMessages returns message history for a user.
// GET /api/test/user/:openid/messages?limit=50
func (t *TestAPI) GetMessages(c *gin.Context) {
	openID := c.Param("openid")

	u, err := t.userService.FindByOpenID(openID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found", "openid": openID})
		return
	}

	limit := 50
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	messages, err := t.userService.GetMessageHistory(u.ID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"openid":   openID,
		"count":    len(messages),
		"messages": messages,
	})
}

// ListUsers lists all registered users.
// GET /api/test/users
func (t *TestAPI) ListUsers(c *gin.Context) {
	users, err := t.userService.ListAll()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"count": len(users),
		"users": users,
	})
}

// DeleteUser deletes a user and their container.
// DELETE /api/test/user/:openid
func (t *TestAPI) DeleteUser(c *gin.Context) {
	openID := c.Param("openid")

	u, err := t.userService.FindByOpenID(openID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found", "openid": openID})
		return
	}

	// Remove container if exists
	if u.ContainerID != "" {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()

		if err := t.containerMgr.RemoveContainer(ctx, u.ContainerID, u.ContainerPort); err != nil {
			logger.Warn("Failed to remove container during user delete",
				"openid", openID, "error", err)
		}
	}

	// Delete user from DB
	if err := t.userService.Delete(openID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "deleted",
		"openid":  openID,
		"message": "User and container removed",
	})
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
