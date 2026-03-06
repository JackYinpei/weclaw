package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qcy/weclaw/internal/api"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/openclaw"
	"github.com/qcy/weclaw/internal/router"
	"github.com/qcy/weclaw/internal/store"
	"github.com/qcy/weclaw/internal/user"
	"github.com/qcy/weclaw/internal/wechat"
	"github.com/qcy/weclaw/pkg/logger"
)

func main() {
	// Initialize logger
	logger.Init("info")
	logger.Info("Starting WeClaw server...")

	// Load configuration
	cfg, err := config.Load("")
	if err != nil {
		logger.Fatal("Failed to load config", "error", err)
	}
	logger.Info("Configuration loaded",
		"port", cfg.Server.Port,
		"max_containers", cfg.Docker.MaxContainers,
	)

	// Initialize database
	db, err := store.New(&cfg.Database)
	if err != nil {
		logger.Fatal("Failed to initialize database", "error", err)
	}
	logger.Info("Database initialized")

	// Initialize Docker container manager
	containerMgr, err := container.NewManager(&cfg.Docker)
	if err != nil {
		logger.Fatal("Failed to initialize Docker manager", "error", err)
	}
	defer containerMgr.Close()
	logger.Info("Docker manager initialized")

	// Initialize services
	userService := user.NewService(db.DB(), &cfg.Quota)
	openclawClient := openclaw.NewClient()
	wechatAPI := wechat.NewAPI(&cfg.WeChat)

	// Initialize message router
	msgRouter := router.NewMessageRouter(userService, containerMgr, openclawClient, wechatAPI, cfg)

	// Initialize WeChat handler
	wechatHandler := wechat.NewHandler(&cfg.WeChat, msgRouter)

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLoggerMiddleware())

	// Simple CORS middleware for local testing
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE, UPDATE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// Serve static files from the 'web' directory
	r.Static("/web", "./web")

	// Register routes
	wechatHandler.RegisterRoutes(r)

	// Register test API routes
	testAPI := api.NewTestAPI(cfg, userService, containerMgr, openclawClient)
	testAPI.RegisterRoutes(r)

	// Health check endpoint
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"service": "weclaw",
			"time":    time.Now().Format(time.RFC3339),
		})
	})

	// Start idle container cleanup routine
	go startIdleContainerCleanup(userService, containerMgr, cfg.Docker.IdleTimeoutMinutes)

	// Start daily quota reset routine
	go startDailyQuotaReset(userService)

	// Start HTTP server
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: r,
	}

	// Graceful shutdown
	go func() {
		logger.Info("WeClaw server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Server failed to start", "error", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatal("Server forced to shutdown", "error", err)
	}

	logger.Info("Server exited")
}

// requestLoggerMiddleware logs incoming HTTP requests.
func requestLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		latency := time.Since(start)
		statusCode := c.Writer.Status()

		logger.Info("HTTP request",
			"method", c.Request.Method,
			"path", path,
			"status", statusCode,
			"latency", latency.String(),
			"client_ip", c.ClientIP(),
		)
	}
}

// startIdleContainerCleanup periodically checks and sleeps idle containers.
func startIdleContainerCleanup(userService *user.Service, containerMgr *container.Manager, idleMinutes int) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		idleUsers, err := userService.GetIdleUsers(idleMinutes)
		if err != nil {
			logger.Error("Failed to get idle users", "error", err)
			continue
		}

		for _, u := range idleUsers {
			if u.ContainerID == "" {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := containerMgr.StopContainer(ctx, u.ContainerID); err != nil {
				logger.Error("Failed to stop idle container",
					"user", u.OpenID, "container", u.ContainerID[:12], "error", err)
			} else {
				_ = userService.UpdateStatus(u.OpenID, user.StatusSleeping)
				logger.Info("Container put to sleep",
					"user", u.OpenID, "container", u.ContainerID[:12])
			}
			cancel()
		}
	}
}

// startDailyQuotaReset resets daily message quotas at midnight.
func startDailyQuotaReset(userService *user.Service) {
	for {
		now := time.Now()
		// Calculate time until next midnight
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		duration := next.Sub(now)

		logger.Info("Next quota reset scheduled", "in", duration.String())
		time.Sleep(duration)

		if err := userService.ResetDailyQuotas(); err != nil {
			logger.Error("Failed to reset daily quotas", "error", err)
		}
	}
}
