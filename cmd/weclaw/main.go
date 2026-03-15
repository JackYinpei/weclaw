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
	"github.com/qcy/weclaw/internal/account"
	"github.com/qcy/weclaw/internal/api"
	"github.com/qcy/weclaw/internal/catalog"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/groupchat"
	"github.com/qcy/weclaw/internal/openclaw"
	"github.com/qcy/weclaw/internal/store"
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
	containerMgr, err := container.NewManager(&cfg.Docker, &cfg.KnowledgeBase)
	if err != nil {
		logger.Fatal("Failed to initialize Docker manager", "error", err)
	}
	defer containerMgr.Close()
	logger.Info("Docker manager initialized")

	// Ensure shared knowledge directory exists
	if err := containerMgr.EnsureKnowledgeDir(); err != nil {
		logger.Fatal("Failed to ensure shared knowledge directory", "error", err)
	}

	// Initialize services
	catalogService := catalog.NewService(db.DB())
	openclawClient := openclaw.NewClient()
	containerService := container.NewService(db.DB(), containerMgr, catalogService, cfg)
	groupChatService := groupchat.NewService(db.DB())

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLoggerMiddleware())

	// CORS middleware
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE, UPDATE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, X-Container-ID")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// Serve static files from the 'web' directory
	r.Static("/web", "./web")

	// Register Auth API routes
	accountRepo := account.NewSQLiteRepository(db.DB())
	authAPI := api.NewAuthAPI(accountRepo)
	authAPI.RegisterRoutes(r)

	// Register Container API routes (CRUD + chat + skills/MCP + store)
	containerAPI := api.NewContainerAPI(cfg, containerService, catalogService, containerMgr, openclawClient, groupChatService, accountRepo)
	containerAPI.RegisterRoutes(r)

	// Register OpenAI compatible API routes
	openaiAPI := api.NewOpenAIAPI(cfg, containerService, containerMgr, openclawClient)
	openaiAPI.RegisterRoutes(r)

	// Health check endpoint
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"service": "weclaw",
			"time":    time.Now().Format(time.RFC3339),
		})
	})

	// Start idle container cleanup routine
	go startIdleContainerCleanup(containerService, containerMgr, cfg.Docker.IdleTimeoutMinutes)

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
func startIdleContainerCleanup(containerService *container.Service, containerMgr *container.Manager, idleMinutes int) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		idleContainers, err := containerService.GetIdleContainers(idleMinutes)
		if err != nil {
			logger.Error("Failed to get idle containers", "error", err)
			continue
		}

		for _, c := range idleContainers {
			if c.ContainerID == "" {
				continue
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			idPrefix := c.ContainerID
			if len(idPrefix) > 12 {
				idPrefix = idPrefix[:12]
			}
			if err := containerMgr.StopContainer(ctx, c.ContainerID); err != nil {
				logger.Error("Failed to stop idle container",
					"id", c.ID, "container", idPrefix, "error", err)
			} else {
				_ = containerService.UpdateStatus(c.ID, "sleeping")
				logger.Info("Container put to sleep",
					"id", c.ID, "container", idPrefix)
			}
			cancel()
		}
	}
}
