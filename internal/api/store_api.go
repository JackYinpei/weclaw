package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qcy/weclaw/internal/catalog"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/user"
	"github.com/qcy/weclaw/pkg/logger"
)

// StoreAPI provides store and user-customization API endpoints.
type StoreAPI struct {
	cfg            *config.Config
	catalogService *catalog.Service
	userService    *user.Service
	containerMgr   *container.Manager
}

// NewStoreAPI creates a new store API handler.
func NewStoreAPI(
	cfg *config.Config,
	catalogService *catalog.Service,
	userService *user.Service,
	containerMgr *container.Manager,
) *StoreAPI {
	return &StoreAPI{
		cfg:            cfg,
		catalogService: catalogService,
		userService:    userService,
		containerMgr:   containerMgr,
	}
}

// RegisterRoutes registers store and user config API routes.
func (s *StoreAPI) RegisterRoutes(r *gin.Engine) {
	store := r.Group("/api/store")
	store.Use(AuthMiddleware())
	{
		store.GET("/skills", s.ListSkillCatalog)
		store.POST("/skills", s.CreateSkillCatalog)
		store.DELETE("/skills/:name", s.DeleteSkillCatalog)
		store.GET("/mcps", s.ListMCPCatalog)
		store.POST("/mcps", s.CreateMCPCatalog)
		store.DELETE("/mcps/:name", s.DeleteMCPCatalog)
		store.GET("/knowledge", s.ListKnowledgeFiles)
		store.GET("/knowledge/read", s.ReadKnowledgeFile)
	}

	userCfg := r.Group("/api/user")
	userCfg.Use(AuthMiddleware())
	{
		userCfg.GET("/skills", s.GetUserSkills)
		userCfg.POST("/skills", s.EnableSkill)
		userCfg.DELETE("/skills/:name", s.DisableSkill)
		userCfg.GET("/mcps", s.GetUserMCPs)
		userCfg.POST("/mcps", s.AddUserMCP)
		userCfg.PUT("/mcps/:name", s.UpdateUserMCP)
		userCfg.DELETE("/mcps/:name", s.RemoveUserMCP)
		userCfg.POST("/apply", s.ApplyChanges)
	}
}

// --- Skill Catalog Endpoints ---

// ListSkillCatalog returns all skills in the store.
func (s *StoreAPI) ListSkillCatalog(c *gin.Context) {
	items, err := s.catalogService.ListSkillCatalog()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

// CreateSkillCatalog adds a skill to the store.
func (s *StoreAPI) CreateSkillCatalog(c *gin.Context) {
	var item catalog.SkillCatalog
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.catalogService.CreateSkillCatalog(&item); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "created", "skill": item})
}

// DeleteSkillCatalog removes a skill from the store.
func (s *StoreAPI) DeleteSkillCatalog(c *gin.Context) {
	name := c.Param("name")
	if err := s.catalogService.DeleteSkillCatalog(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "name": name})
}

// --- MCP Catalog Endpoints ---

// ListMCPCatalog returns all MCP servers in the store.
func (s *StoreAPI) ListMCPCatalog(c *gin.Context) {
	items, err := s.catalogService.ListMCPCatalog()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

// CreateMCPCatalog adds an MCP server to the store.
func (s *StoreAPI) CreateMCPCatalog(c *gin.Context) {
	var item catalog.MCPCatalog
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.catalogService.CreateMCPCatalog(&item); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "created", "mcp": item})
}

// DeleteMCPCatalog removes an MCP server from the store.
func (s *StoreAPI) DeleteMCPCatalog(c *gin.Context) {
	name := c.Param("name")
	if err := s.catalogService.DeleteMCPCatalog(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "name": name})
}

// --- User Skill Endpoints ---

func (s *StoreAPI) resolveUser(c *gin.Context) (*user.User, bool) {
	openid := c.Query("openid")
	if openid == "" {
		// Try from JSON body (for POST/PUT)
		var body struct {
			OpenID string `json:"openid"`
		}
		if err := c.ShouldBindJSON(&body); err == nil && body.OpenID != "" {
			openid = body.OpenID
		}
	}
	if openid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "openid is required"})
		return nil, false
	}
	u, err := s.userService.FindByOpenID(openid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found", "openid": openid})
		return nil, false
	}
	return u, true
}

// GetUserSkills returns the user's enabled skills.
func (s *StoreAPI) GetUserSkills(c *gin.Context) {
	u, ok := s.resolveUser(c)
	if !ok {
		return
	}
	items, err := s.catalogService.GetUserSkills(u.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

type enableSkillReq struct {
	OpenID    string          `json:"openid" binding:"required"`
	SkillName string          `json:"skill_name" binding:"required"`
	Config    json.RawMessage `json:"config"`
}

// EnableSkill enables a skill for the user.
func (s *StoreAPI) EnableSkill(c *gin.Context) {
	var req enableSkillReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, err := s.userService.FindByOpenID(req.OpenID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	cfgStr := ""
	if len(req.Config) > 0 {
		cfgStr = string(req.Config)
	}
	if err := s.catalogService.EnableSkill(u.ID, req.SkillName, cfgStr); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "enabled", "skill": req.SkillName})
}

// DisableSkill disables a skill for the user.
func (s *StoreAPI) DisableSkill(c *gin.Context) {
	name := c.Param("name")
	openid := c.Query("openid")
	if openid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "openid query param is required"})
		return
	}
	u, err := s.userService.FindByOpenID(openid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err := s.catalogService.DisableSkill(u.ID, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "disabled", "skill": name})
}

// --- User MCP Endpoints ---

// GetUserMCPs returns the user's enabled MCP servers.
func (s *StoreAPI) GetUserMCPs(c *gin.Context) {
	u, ok := s.resolveUser(c)
	if !ok {
		return
	}
	items, err := s.catalogService.GetUserMCPs(u.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

type addMCPReq struct {
	OpenID  string          `json:"openid" binding:"required"`
	MCPName string          `json:"mcp_name" binding:"required"`
	Command string          `json:"command"`
	Args    json.RawMessage `json:"args"`
	Env     json.RawMessage `json:"env"`
}

// AddUserMCP adds an MCP server for the user.
func (s *StoreAPI) AddUserMCP(c *gin.Context) {
	var req addMCPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, err := s.userService.FindByOpenID(req.OpenID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// If no command specified, try to fill from catalog
	command := req.Command
	argsStr := string(req.Args)
	envStr := string(req.Env)
	if command == "" {
		mcpCatalog, _ := s.catalogService.ListMCPCatalog()
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
	if err := s.catalogService.AddUserMCP(u.ID, mcp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "added", "mcp": req.MCPName})
}

type updateMCPReq struct {
	OpenID  string          `json:"openid" binding:"required"`
	Command string          `json:"command"`
	Args    json.RawMessage `json:"args"`
	Env     json.RawMessage `json:"env"`
}

// UpdateUserMCP updates an MCP server config for the user.
func (s *StoreAPI) UpdateUserMCP(c *gin.Context) {
	name := c.Param("name")
	var req updateMCPReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, err := s.userService.FindByOpenID(req.OpenID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
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
	if err := s.catalogService.UpdateUserMCP(u.ID, name, updates); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "updated", "mcp": name})
}

// RemoveUserMCP removes an MCP server for the user.
func (s *StoreAPI) RemoveUserMCP(c *gin.Context) {
	name := c.Param("name")
	openid := c.Query("openid")
	if openid == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "openid query param is required"})
		return
	}
	u, err := s.userService.FindByOpenID(openid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if err := s.catalogService.RemoveUserMCP(u.ID, name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "removed", "mcp": name})
}

// --- Apply Changes ---

type applyReq struct {
	OpenID string `json:"openid" binding:"required"`
}

// ApplyChanges regenerates openclaw.json with user's skill/MCP config and restarts the container.
func (s *StoreAPI) ApplyChanges(c *gin.Context) {
	var req applyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	u, err := s.userService.FindByOpenID(req.OpenID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	if u.ContainerID == "" || u.ContainerName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user has no container"})
		return
	}

	// Build extras from DB
	skills, skillDirs, mcps, buildErr := s.catalogService.BuildOpenClawExtras(u.ID)
	if buildErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("build extras: %v", buildErr)})
		return
	}

	var extras *container.OpenClawExtras
	if len(skills) > 0 || len(mcps) > 0 {
		extras = &container.OpenClawExtras{
			Skills:    skills,
			SkillDirs: skillDirs,
			MCPs:      mcps,
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	if err := s.containerMgr.RegenerateConfig(ctx, u.ContainerName, u.ContainerID, &s.cfg.OpenClaw, extras); err != nil {
		logger.Error("Failed to apply changes", "user", req.OpenID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("apply failed: %v", err)})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "applied",
		"openid":  req.OpenID,
		"message": "Configuration updated and container restarted",
	})
}

// --- Knowledge Base Endpoints ---

type knowledgeEntry struct {
	Name  string           `json:"name"`
	Path  string           `json:"path"`
	IsDir bool             `json:"is_dir"`
	Size  int64            `json:"size,omitempty"`
	Items []knowledgeEntry `json:"items,omitempty"`
}

// ListKnowledgeFiles returns the file tree of the shared knowledge directory.
func (s *StoreAPI) ListKnowledgeFiles(c *gin.Context) {
	hostDir := s.cfg.KnowledgeBase.HostDir
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
			continue // skip hidden files
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

// ReadKnowledgeFile reads a text file from the shared knowledge directory.
func (s *StoreAPI) ReadKnowledgeFile(c *gin.Context) {
	hostDir := s.cfg.KnowledgeBase.HostDir
	reqPath := c.Query("path")
	if hostDir == "" || reqPath == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}

	// Prevent path traversal
	cleanPath := filepath.Clean(reqPath)
	if strings.Contains(cleanPath, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}

	fullPath := filepath.Join(hostDir, cleanPath)

	// Ensure it's within the knowledge dir
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

	// Limit read size to 1MB
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
