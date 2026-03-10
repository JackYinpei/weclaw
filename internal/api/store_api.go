package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qcy/weclaw/internal/catalog"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/openclaw"
	"github.com/qcy/weclaw/pkg/logger"
)

// ContainerAPI provides container CRUD, chat, skill/MCP, and store endpoints.
type ContainerAPI struct {
	cfg              *config.Config
	containerService *container.Service
	catalogService   *catalog.Service
	containerMgr     *container.Manager
	openclawClient   *openclaw.Client
}

// NewContainerAPI creates a new container API handler.
func NewContainerAPI(
	cfg *config.Config,
	containerService *container.Service,
	catalogService *catalog.Service,
	containerMgr *container.Manager,
	openclawClient *openclaw.Client,
) *ContainerAPI {
	return &ContainerAPI{
		cfg:              cfg,
		containerService: containerService,
		catalogService:   catalogService,
		containerMgr:     containerMgr,
		openclawClient:   openclawClient,
	}
}

// RegisterRoutes registers all container and store API routes.
func (api *ContainerAPI) RegisterRoutes(r *gin.Engine) {
	// WebSocket endpoint (JWT via query param, handler does its own auth)
	r.GET("/ws/containers/:id", api.HandleWebSocket)

	// Container CRUD + chat + skills/MCP
	containers := r.Group("/api/containers")
	containers.Use(AuthMiddleware())
	{
		containers.GET("", api.ListContainers)
		containers.POST("", api.CreateContainer)
		containers.GET("/:id", api.GetContainer)
		containers.DELETE("/:id", api.DeleteContainer)
		containers.POST("/:id/send", api.SendMessage)
		containers.GET("/:id/messages", api.GetMessages)
		containers.GET("/:id/gateway-status", api.GatewayStatus)
		containers.GET("/:id/skills", api.GetContainerSkills)
		containers.POST("/:id/skills", api.EnableSkill)
		containers.DELETE("/:id/skills/:name", api.DisableSkill)
		containers.GET("/:id/mcps", api.GetContainerMCPs)
		containers.POST("/:id/mcps", api.AddContainerMCP)
		containers.PUT("/:id/mcps/:name", api.UpdateContainerMCP)
		containers.DELETE("/:id/mcps/:name", api.RemoveContainerMCP)
		containers.POST("/:id/apply", api.ApplyChanges)
		containers.POST("/:id/restart", api.RestartContainer)
	}

	// Store catalog (admin)
	store := r.Group("/api/store")
	store.Use(AuthMiddleware())
	{
		store.GET("/skills", api.ListSkillCatalog)
		store.POST("/skills", api.CreateSkillCatalog)
		store.DELETE("/skills/:name", api.DeleteSkillCatalog)
		store.GET("/mcps", api.ListMCPCatalog)
		store.POST("/mcps", api.CreateMCPCatalog)
		store.DELETE("/mcps/:name", api.DeleteMCPCatalog)
		store.GET("/knowledge", api.ListKnowledgeFiles)
		store.GET("/knowledge/read", api.ReadKnowledgeFile)
	}
}

// --- Helpers ---

func getAccountID(c *gin.Context) uint {
	sub, _ := c.Get("userID")
	// JWT sub is stored as float64 by default
	switch v := sub.(type) {
	case float64:
		return uint(v)
	case uint:
		return v
	case int:
		return uint(v)
	default:
		return 0
	}
}

func (api *ContainerAPI) resolveContainer(c *gin.Context) (*container.Container, bool) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid container id"})
		return nil, false
	}
	accountID := getAccountID(c)
	ctr, err := api.containerService.GetByID(uint(id), accountID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "container not found"})
		return nil, false
	}
	return ctr, true
}

// --- Container CRUD ---

// ListContainers returns all containers for the current account.
func (api *ContainerAPI) ListContainers(c *gin.Context) {
	accountID := getAccountID(c)
	containers, err := api.containerService.ListByAccount(accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Enrich with running status
	type containerWithRunning struct {
		container.Container
		IsRunning bool `json:"is_running"`
	}
	var result []containerWithRunning
	for _, ctr := range containers {
		running, _ := api.containerService.IsContainerRunning(&ctr)
		result = append(result, containerWithRunning{Container: ctr, IsRunning: running})
	}

	c.JSON(http.StatusOK, gin.H{"containers": result})
}

type createContainerReq struct {
	DisplayName string `json:"display_name" binding:"required"`
}

// CreateContainer creates a new container for the current account.
func (api *ContainerAPI) CreateContainer(c *gin.Context) {
	var req createContainerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	accountID := getAccountID(c)
	ctr, err := api.containerService.Create(accountID, req.DisplayName)
	if err != nil {
		logger.Error("Failed to create container", "account_id", accountID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "created", "container": ctr})
}

// GetContainer returns a single container with Docker status.
func (api *ContainerAPI) GetContainer(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	running, _ := api.containerService.IsContainerRunning(ctr)
	c.JSON(http.StatusOK, gin.H{"container": ctr, "is_running": running})
}

// DeleteContainer removes a container.
func (api *ContainerAPI) DeleteContainer(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid container id"})
		return
	}
	accountID := getAccountID(c)
	if err := api.containerService.Delete(uint(id), accountID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

// --- Chat ---

type sendMessageReq struct {
	Message string `json:"message" binding:"required"`
}

// SendMessage sends a message to the container's OpenClaw instance.
func (api *ContainerAPI) SendMessage(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}

	var req sendMessageReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if ctr.ContainerID == "" || ctr.ContainerPort == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "container has no Docker instance"})
		return
	}

	// Wake up if sleeping
	if err := api.containerService.EnsureRunning(ctr); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	startTime := time.Now()
	response, err := api.openclawClient.SendMessage(ctx, ctr.ContainerPort, ctr.GatewayToken, req.Message)
	elapsed := time.Since(startTime)

	if err != nil {
		logger.Error("OpenClaw send error", "container_id", ctr.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("OpenClaw error: %v", err)})
		return
	}

	// Log and update activity
	_ = api.containerService.LogMessage(ctr.ID, "incoming", "text", req.Message)
	_ = api.containerService.LogMessage(ctr.ID, "outgoing", "text", response)
	_ = api.containerService.TouchActivity(ctr.ID)

	c.JSON(http.StatusOK, gin.H{
		"response": response,
		"elapsed":  elapsed.String(),
	})
}

// GetMessages returns message history for a container.
func (api *ContainerAPI) GetMessages(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}

	limit := 50
	if l := c.Query("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}

	messages, err := api.containerService.GetMessageHistory(ctr.ID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"container_id": ctr.ID,
		"count":        len(messages),
		"messages":     messages,
	})
}

// GatewayStatus checks if the container's OpenClaw Gateway is ready to accept requests.
func (api *ContainerAPI) GatewayStatus(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}

	if ctr.ContainerPort == 0 || ctr.GatewayToken == "" {
		c.JSON(http.StatusOK, gin.H{"ready": false, "reason": "no_port"})
		return
	}

	ready := api.openclawClient.CheckHealth(ctr.ContainerPort, ctr.GatewayToken)
	c.JSON(http.StatusOK, gin.H{"ready": ready})
}

// --- Container Skills ---

// GetContainerSkills returns the container's enabled skills.
func (api *ContainerAPI) GetContainerSkills(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	items, err := api.catalogService.GetContainerSkills(ctr.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

type enableSkillReq struct {
	SkillName string          `json:"skill_name" binding:"required"`
	Config    json.RawMessage `json:"config"`
}

// EnableSkill enables a skill for the container.
func (api *ContainerAPI) EnableSkill(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	var req enableSkillReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cfgStr := ""
	if len(req.Config) > 0 {
		cfgStr = string(req.Config)
	}
	if err := api.catalogService.EnableSkill(ctr.ID, req.SkillName, cfgStr); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "enabled", "skill": req.SkillName})
}

// DisableSkill disables a skill for the container.
func (api *ContainerAPI) DisableSkill(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	name := c.Param("name")
	if err := api.catalogService.DisableSkill(ctr.ID, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "disabled", "skill": name})
}

// --- Container MCPs ---

// GetContainerMCPs returns the container's enabled MCP servers.
func (api *ContainerAPI) GetContainerMCPs(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	items, err := api.catalogService.GetContainerMCPs(ctr.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

type addMCPReq struct {
	MCPName string          `json:"mcp_name" binding:"required"`
	Command string          `json:"command"`
	Args    json.RawMessage `json:"args"`
	Env     json.RawMessage `json:"env"`
}

// AddContainerMCP adds an MCP server for the container.
func (api *ContainerAPI) AddContainerMCP(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	var req addMCPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// If no command specified, try to fill from catalog
	command := req.Command
	argsStr := string(req.Args)
	envStr := string(req.Env)
	if command == "" {
		mcpCatalog, _ := api.catalogService.ListMCPCatalog()
		for _, mc := range mcpCatalog {
			if mc.Name == req.MCPName {
				command = mc.Command
				if argsStr == "" || argsStr == "null" {
					argsStr = mc.Args
				}
				if envStr == "" || envStr == "null" {
					envStr = mc.DefaultEnv
				}
				break
			}
		}
	}

	mcp := &catalog.UserMCP{
		MCPName: req.MCPName,
		Enabled: true,
		Command: command,
		Args:    argsStr,
		Env:     envStr,
	}
	if err := api.catalogService.AddContainerMCP(ctr.ID, mcp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "added", "mcp": req.MCPName})
}

type updateMCPReq struct {
	Command string          `json:"command"`
	Args    json.RawMessage `json:"args"`
	Env     json.RawMessage `json:"env"`
}

// UpdateContainerMCP updates an MCP server config for the container.
func (api *ContainerAPI) UpdateContainerMCP(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	name := c.Param("name")
	var req updateMCPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updates := make(map[string]any)
	if req.Command != "" {
		updates["command"] = req.Command
	}
	if len(req.Args) > 0 {
		updates["args"] = string(req.Args)
	}
	if len(req.Env) > 0 {
		updates["env"] = string(req.Env)
	}
	if err := api.catalogService.UpdateContainerMCP(ctr.ID, name, updates); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "updated", "mcp": name})
}

// RemoveContainerMCP removes an MCP server for the container.
func (api *ContainerAPI) RemoveContainerMCP(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	name := c.Param("name")
	if err := api.catalogService.RemoveContainerMCP(ctr.ID, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "removed", "mcp": name})
}

// --- Apply Changes ---

// ApplyChanges regenerates openclaw.json with container's skill/MCP config and restarts.
func (api *ContainerAPI) ApplyChanges(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	if ctr.ContainerID == "" || ctr.ContainerName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "container has no Docker instance"})
		return
	}
	if err := api.containerService.ApplyChanges(ctr); err != nil {
		logger.Error("Failed to apply changes", "container_id", ctr.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "applied",
		"message": "Configuration updated and container restarted",
	})
}

// RestartContainer restarts a container's Docker instance to recover crashed gateway.
func (api *ContainerAPI) RestartContainer(c *gin.Context) {
	ctr, ok := api.resolveContainer(c)
	if !ok {
		return
	}
	if err := api.containerService.RestartContainer(ctr); err != nil {
		logger.Error("Failed to restart container", "container_id", ctr.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":  "restarted",
		"message": "Container restarted, gateway should recover in a few seconds",
	})
}

// --- Skill Catalog Endpoints ---

func (api *ContainerAPI) ListSkillCatalog(c *gin.Context) {
	items, err := api.catalogService.ListSkillCatalog()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (api *ContainerAPI) CreateSkillCatalog(c *gin.Context) {
	var item catalog.SkillCatalog
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := api.catalogService.CreateSkillCatalog(&item); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "created", "skill": item})
}

func (api *ContainerAPI) DeleteSkillCatalog(c *gin.Context) {
	name := c.Param("name")
	if err := api.catalogService.DeleteSkillCatalog(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "name": name})
}

// --- MCP Catalog Endpoints ---

func (api *ContainerAPI) ListMCPCatalog(c *gin.Context) {
	items, err := api.catalogService.ListMCPCatalog()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (api *ContainerAPI) CreateMCPCatalog(c *gin.Context) {
	var item catalog.MCPCatalog
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := api.catalogService.CreateMCPCatalog(&item); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "created", "mcp": item})
}

func (api *ContainerAPI) DeleteMCPCatalog(c *gin.Context) {
	name := c.Param("name")
	if err := api.catalogService.DeleteMCPCatalog(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "name": name})
}

// --- Knowledge Base Endpoints ---

type knowledgeEntry struct {
	Name  string           `json:"name"`
	Path  string           `json:"path"`
	IsDir bool             `json:"is_dir"`
	Size  int64            `json:"size,omitempty"`
	Items []knowledgeEntry `json:"items,omitempty"`
}

func (api *ContainerAPI) ListKnowledgeFiles(c *gin.Context) {
	hostDir := api.cfg.KnowledgeBase.HostDir
	if hostDir == "" {
		c.JSON(http.StatusOK, []knowledgeEntry{})
		return
	}

	entries, err := scanDir(hostDir, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, entries)
}

func scanDir(baseDir, relPath string) ([]knowledgeEntry, error) {
	fullPath := filepath.Join(baseDir, relPath)
	dirEntries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	var result []knowledgeEntry
	for _, de := range dirEntries {
		name := de.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		entryPath := filepath.Join(relPath, name)
		entry := knowledgeEntry{
			Name:  name,
			Path:  entryPath,
			IsDir: de.IsDir(),
		}
		if de.IsDir() {
			children, err := scanDir(baseDir, entryPath)
			if err == nil {
				entry.Items = children
			}
		} else {
			if info, err := de.Info(); err == nil {
				entry.Size = info.Size()
			}
		}
		result = append(result, entry)
	}
	return result, nil
}

func (api *ContainerAPI) ReadKnowledgeFile(c *gin.Context) {
	hostDir := api.cfg.KnowledgeBase.HostDir
	reqPath := c.Query("path")
	if hostDir == "" || reqPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}

	cleanPath := filepath.Clean(reqPath)
	if strings.Contains(cleanPath, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}

	fullPath := filepath.Join(hostDir, cleanPath)

	absKB, _ := filepath.Abs(hostDir)
	absFile, _ := filepath.Abs(fullPath)
	if !strings.HasPrefix(absFile, absKB) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path outside knowledge base"})
		return
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is a directory"})
		return
	}

	if info.Size() > 1024*1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file too large (max 1MB)"})
		return
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"path":    cleanPath,
		"name":    filepath.Base(cleanPath),
		"size":    info.Size(),
		"content": string(data),
	})
}
