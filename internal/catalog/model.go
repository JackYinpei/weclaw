package catalog

import (
	"time"

	"gorm.io/gorm"
)

// SkillCatalog is an admin-curated skill available in the store.
type SkillCatalog struct {
	ID            uint           `gorm:"primarykey" json:"id"`
	Name          string         `gorm:"uniqueIndex;size:128;not null" json:"name"`
	DisplayName   string         `gorm:"size:256" json:"display_name"`
	Description   string         `gorm:"type:text" json:"description"`
	Category      string         `gorm:"size:64" json:"category"`
	Icon          string         `gorm:"size:32" json:"icon"`
	DefaultConfig string         `gorm:"type:text" json:"default_config"` // JSON string
	SkillDir      string         `gorm:"size:512" json:"skill_dir"`      // path inside shared volume
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

// UserSkill is a user's enabled skill with optional config overrides.
type UserSkill struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	UserID    uint           `gorm:"uniqueIndex:idx_user_skill;not null" json:"user_id"`
	SkillName string         `gorm:"uniqueIndex:idx_user_skill;size:128;not null" json:"skill_name"`
	Enabled   bool           `gorm:"default:true" json:"enabled"`
	Config    string         `gorm:"type:text" json:"config"` // JSON override
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// MCPCatalog is an admin-curated MCP server available in the store.
type MCPCatalog struct {
	ID          uint           `gorm:"primarykey" json:"id"`
	Name        string         `gorm:"uniqueIndex;size:128;not null" json:"name"`
	DisplayName string         `gorm:"size:256" json:"display_name"`
	Description string         `gorm:"type:text" json:"description"`
	Category    string         `gorm:"size:64" json:"category"`
	Icon        string         `gorm:"size:32" json:"icon"`
	Command     string         `gorm:"size:256;not null" json:"command"`
	Args        string         `gorm:"type:text" json:"args"`        // JSON array
	DefaultEnv  string         `gorm:"type:text" json:"default_env"` // JSON object
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// UserMCP is a user's enabled MCP server with env overrides.
type UserMCP struct {
	ID        uint           `gorm:"primarykey" json:"id"`
	UserID    uint           `gorm:"uniqueIndex:idx_user_mcp;not null" json:"user_id"`
	MCPName   string         `gorm:"uniqueIndex:idx_user_mcp;size:128;not null" json:"mcp_name"`
	Enabled   bool           `gorm:"default:true" json:"enabled"`
	Command   string         `gorm:"size:256" json:"command"`
	Args      string         `gorm:"type:text" json:"args"` // JSON array
	Env       string         `gorm:"type:text" json:"env"`  // JSON object
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}
