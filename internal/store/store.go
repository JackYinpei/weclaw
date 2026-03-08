package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/qcy/weclaw/internal/catalog"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/internal/user"
	"github.com/qcy/weclaw/pkg/logger"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Store wraps the database connection and provides data access methods.
type Store struct {
	db *gorm.DB
}

// New creates a new Store and initializes the database.
func New(cfg *config.DatabaseConfig) (*Store, error) {
	// Ensure data directory exists
	dir := filepath.Dir(cfg.DSN)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(cfg.DSN), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Auto-migrate tables
	if err := db.AutoMigrate(
		&user.User{}, &user.MessageLog{},
		&catalog.SkillCatalog{}, &catalog.UserSkill{},
		&catalog.MCPCatalog{}, &catalog.UserMCP{},
	); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	logger.Info("Database initialized", "driver", cfg.Driver, "dsn", cfg.DSN)

	return &Store{db: db}, nil
}

// DB returns the underlying gorm.DB instance.
func (s *Store) DB() *gorm.DB {
	return s.db
}
