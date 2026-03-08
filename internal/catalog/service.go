package catalog

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// Service provides catalog and user-selection CRUD operations.
type Service struct {
	db *gorm.DB
}

// NewService creates a new catalog service.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// --- Skill Catalog (admin) ---

// ListSkillCatalog returns all skills in the store.
func (s *Service) ListSkillCatalog() ([]SkillCatalog, error) {
	var items []SkillCatalog
	if err := s.db.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// CreateSkillCatalog adds a skill to the store.
func (s *Service) CreateSkillCatalog(item *SkillCatalog) error {
	return s.db.Create(item).Error
}

// DeleteSkillCatalog removes a skill from the store by name.
func (s *Service) DeleteSkillCatalog(name string) error {
	return s.db.Where("name = ?", name).Delete(&SkillCatalog{}).Error
}

// --- MCP Catalog (admin) ---

// ListMCPCatalog returns all MCP servers in the store.
func (s *Service) ListMCPCatalog() ([]MCPCatalog, error) {
	var items []MCPCatalog
	if err := s.db.Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// CreateMCPCatalog adds an MCP server to the store.
func (s *Service) CreateMCPCatalog(item *MCPCatalog) error {
	return s.db.Create(item).Error
}

// DeleteMCPCatalog removes an MCP server from the store by name.
func (s *Service) DeleteMCPCatalog(name string) error {
	return s.db.Where("name = ?", name).Delete(&MCPCatalog{}).Error
}

// --- User Skills ---

// GetUserSkills returns all skills a user has enabled.
func (s *Service) GetUserSkills(userID uint) ([]UserSkill, error) {
	var items []UserSkill
	if err := s.db.Where("user_id = ?", userID).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// EnableSkill enables a skill for a user, creating or updating the record.
func (s *Service) EnableSkill(userID uint, skillName string, config string) error {
	var existing UserSkill
	err := s.db.Where("user_id = ? AND skill_name = ?", userID, skillName).First(&existing).Error
	if err == nil {
		// Update existing
		return s.db.Model(&existing).Updates(map[string]any{
			"enabled": true,
			"config":  config,
		}).Error
	}
	if err != gorm.ErrRecordNotFound {
		return err
	}
	// Create new
	return s.db.Create(&UserSkill{
		UserID:    userID,
		SkillName: skillName,
		Enabled:   true,
		Config:    config,
	}).Error
}

// DisableSkill disables (soft-deletes) a skill for a user.
func (s *Service) DisableSkill(userID uint, skillName string) error {
	return s.db.Where("user_id = ? AND skill_name = ?", userID, skillName).Delete(&UserSkill{}).Error
}

// --- User MCPs ---

// GetUserMCPs returns all MCP servers a user has enabled.
func (s *Service) GetUserMCPs(userID uint) ([]UserMCP, error) {
	var items []UserMCP
	if err := s.db.Where("user_id = ?", userID).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// AddUserMCP adds an MCP server for a user.
func (s *Service) AddUserMCP(userID uint, mcp *UserMCP) error {
	mcp.UserID = userID
	return s.db.Create(mcp).Error
}

// UpdateUserMCP updates an MCP server config for a user.
func (s *Service) UpdateUserMCP(userID uint, mcpName string, updates map[string]any) error {
	result := s.db.Model(&UserMCP{}).Where("user_id = ? AND mcp_name = ?", userID, mcpName).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("MCP server %q not found for user", mcpName)
	}
	return nil
}

// RemoveUserMCP removes an MCP server for a user.
func (s *Service) RemoveUserMCP(userID uint, mcpName string) error {
	return s.db.Where("user_id = ? AND mcp_name = ?", userID, mcpName).Delete(&UserMCP{}).Error
}

// --- Extras Builder ---

// BuildOpenClawExtras builds the extras map from user's skill/MCP selections.
// Returns skills entries, skill extra dirs, and MCP server configs.
func (s *Service) BuildOpenClawExtras(userID uint) (
	skills map[string]map[string]any,
	skillDirs []string,
	mcps map[string]map[string]any,
	err error,
) {
	// Fetch user skills
	userSkills, err := s.GetUserSkills(userID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get user skills: %w", err)
	}

	// Fetch skill catalog for skill dirs
	catalog, err := s.ListSkillCatalog()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list skill catalog: %w", err)
	}
	catalogMap := make(map[string]SkillCatalog)
	for _, c := range catalog {
		catalogMap[c.Name] = c
	}

	// Build skill entries
	skills = make(map[string]map[string]any)
	skillDirSet := make(map[string]bool)
	for _, us := range userSkills {
		entry := map[string]any{"enabled": us.Enabled}
		if us.Config != "" {
			var cfg map[string]any
			if json.Unmarshal([]byte(us.Config), &cfg) == nil {
				entry["config"] = cfg
			}
		}
		skills[us.SkillName] = entry

		// Collect skill dirs from catalog
		if sc, ok := catalogMap[us.SkillName]; ok && sc.SkillDir != "" {
			skillDirSet[sc.SkillDir] = true
		}
	}
	for dir := range skillDirSet {
		skillDirs = append(skillDirs, dir)
	}

	// Fetch user MCPs
	userMCPs, err := s.GetUserMCPs(userID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get user mcps: %w", err)
	}

	mcps = make(map[string]map[string]any)
	for _, um := range userMCPs {
		entry := map[string]any{"command": um.Command}
		if um.Args != "" {
			var args []string
			if json.Unmarshal([]byte(um.Args), &args) == nil {
				entry["args"] = args
			}
		}
		if um.Env != "" {
			var env map[string]string
			if json.Unmarshal([]byte(um.Env), &env) == nil {
				entry["env"] = env
			}
		}
		mcps[um.MCPName] = entry
	}

	return skills, skillDirs, mcps, nil
}
