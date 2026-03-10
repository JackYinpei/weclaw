package catalog

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// Service provides catalog and container-selection CRUD operations.
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

// --- Container Skills ---

// GetContainerSkills returns all skills a container has enabled.
func (s *Service) GetContainerSkills(containerID uint) ([]UserSkill, error) {
	var items []UserSkill
	if err := s.db.Where("container_id = ?", containerID).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// EnableSkill enables a skill for a container, creating or updating the record.
func (s *Service) EnableSkill(containerID uint, skillName string, config string) error {
	var existing UserSkill
	err := s.db.Where("container_id = ? AND skill_name = ?", containerID, skillName).First(&existing).Error
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
		ContainerID: containerID,
		SkillName:   skillName,
		Enabled:     true,
		Config:      config,
	}).Error
}

// DisableSkill disables (soft-deletes) a skill for a container.
func (s *Service) DisableSkill(containerID uint, skillName string) error {
	return s.db.Where("container_id = ? AND skill_name = ?", containerID, skillName).Delete(&UserSkill{}).Error
}

// --- Container MCPs ---

// GetContainerMCPs returns all MCP servers a container has enabled.
func (s *Service) GetContainerMCPs(containerID uint) ([]UserMCP, error) {
	var items []UserMCP
	if err := s.db.Where("container_id = ?", containerID).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// AddContainerMCP adds an MCP server for a container.
func (s *Service) AddContainerMCP(containerID uint, mcp *UserMCP) error {
	mcp.ContainerID = containerID
	return s.db.Create(mcp).Error
}

// UpdateContainerMCP updates an MCP server config for a container.
func (s *Service) UpdateContainerMCP(containerID uint, mcpName string, updates map[string]any) error {
	result := s.db.Model(&UserMCP{}).Where("container_id = ? AND mcp_name = ?", containerID, mcpName).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("MCP server %q not found for container", mcpName)
	}
	return nil
}

// RemoveContainerMCP removes an MCP server for a container.
func (s *Service) RemoveContainerMCP(containerID uint, mcpName string) error {
	return s.db.Where("container_id = ? AND mcp_name = ?", containerID, mcpName).Delete(&UserMCP{}).Error
}

// --- Extras Builder ---

// BuildOpenClawExtras builds the extras map from a container's skill/MCP selections.
func (s *Service) BuildOpenClawExtras(containerID uint) (
	skills map[string]map[string]any,
	skillDirs []string,
	mcps map[string]map[string]any,
	err error,
) {
	// Fetch container skills
	containerSkills, err := s.GetContainerSkills(containerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get container skills: %w", err)
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
	for _, us := range containerSkills {
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

	// Fetch container MCPs
	containerMCPs, err := s.GetContainerMCPs(containerID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("get container mcps: %w", err)
	}

	mcps = make(map[string]map[string]any)
	for _, um := range containerMCPs {
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
