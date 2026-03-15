package container

import (
	"context"
	"fmt"
	"time"

	"github.com/qcy/weclaw/internal/catalog"
	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/pkg/logger"
	"gorm.io/gorm"
)

// Service provides business-layer CRUD for containers.
type Service struct {
	db             *gorm.DB
	mgr            *Manager
	catalogService *catalog.Service
	cfg            *config.Config
}

// NewService creates a new container service.
func NewService(db *gorm.DB, mgr *Manager, catalogService *catalog.Service, cfg *config.Config) *Service {
	return &Service{
		db:             db,
		mgr:            mgr,
		catalogService: catalogService,
		cfg:            cfg,
	}
}

// ListByAccount returns all containers belonging to an account.
func (s *Service) ListByAccount(accountID uint) ([]Container, error) {
	var containers []Container
	if err := s.db.Where("account_id = ?", accountID).Order("created_at DESC").Find(&containers).Error; err != nil {
		return nil, err
	}
	return containers, nil
}

// GetByID returns a container by ID with ownership check.
func (s *Service) GetByID(id uint, accountID uint) (*Container, error) {
	var c Container
	if err := s.db.Where("id = ? AND account_id = ?", id, accountID).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// GetFirstActiveByAccount returns the first (oldest) container for an account.
func (s *Service) GetFirstActiveByAccount(accountID uint) (*Container, error) {
	var c Container
	err := s.db.Where("account_id = ?", accountID).Order("created_at ASC").First(&c).Error
	return &c, err
}

// GetByIDNoOwnerCheck returns a container by ID without ownership check (for cross-account lookups like group chat).
func (s *Service) GetByIDNoOwnerCheck(id uint) (*Container, error) {
	var c Container
	if err := s.db.First(&c, id).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// Create creates a new container for an account.
func (s *Service) Create(accountID uint, displayName string) (*Container, error) {
	c := &Container{
		AccountID:   accountID,
		DisplayName: displayName,
		Status:      "pending",
	}
	if err := s.db.Create(c).Error; err != nil {
		return nil, fmt.Errorf("failed to create container record: %w", err)
	}

	// Build extras from any pre-configured skills/MCPs (empty for new container)
	extras := s.buildExtras(c.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Use container DB ID as unique identifier for Docker container naming
	identifier := fmt.Sprintf("acct%d-ctr%d", accountID, c.ID)
	info, err := s.mgr.CreateContainer(ctx, identifier, &s.cfg.OpenClaw, extras)
	if err != nil {
		// Mark as failed but keep the record
		s.db.Model(c).Updates(map[string]any{"status": "disabled"})
		return c, fmt.Errorf("failed to create Docker container: %w", err)
	}

	// Update with Docker info
	now := time.Now()
	if err := s.db.Model(c).Updates(map[string]any{
		"container_id":   info.ContainerID,
		"container_name": info.ContainerName,
		"container_port": info.Port,
		"gateway_token":  info.GatewayToken,
		"status":         "active",
		"last_active_at": &now,
	}).Error; err != nil {
		return nil, fmt.Errorf("failed to update container info: %w", err)
	}

	c.ContainerID = info.ContainerID
	c.ContainerName = info.ContainerName
	c.ContainerPort = info.Port
	c.GatewayToken = info.GatewayToken
	c.Status = "active"
	c.LastActiveAt = &now

	logger.Info("Container created", "id", c.ID, "account_id", accountID, "display_name", displayName, "docker_name", info.ContainerName)
	return c, nil
}

// Delete removes a container and its Docker instance.
func (s *Service) Delete(id uint, accountID uint) error {
	c, err := s.GetByID(id, accountID)
	if err != nil {
		return fmt.Errorf("container not found: %w", err)
	}

	// Remove Docker container
	if c.ContainerID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.mgr.RemoveContainer(ctx, c.ContainerID, c.ContainerPort); err != nil {
			logger.Warn("Failed to remove Docker container", "id", c.ID, "error", err)
		}
	}

	// Hard delete from DB (including associated skills/MCPs)
	s.db.Unscoped().Where("container_id = ?", c.ID).Delete(&catalog.UserSkill{})
	s.db.Unscoped().Where("container_id = ?", c.ID).Delete(&catalog.UserMCP{})
	s.db.Unscoped().Where("container_id = ?", c.ID).Delete(&MessageLog{})
	if err := s.db.Unscoped().Delete(c).Error; err != nil {
		return fmt.Errorf("failed to delete container: %w", err)
	}

	logger.Info("Container deleted", "id", c.ID, "account_id", accountID)
	return nil
}

// UpdateStatus updates a container's status.
func (s *Service) UpdateStatus(id uint, status string) error {
	return s.db.Model(&Container{}).Where("id = ?", id).Update("status", status).Error
}

// UpdateAllowMention updates a container's allow_mention flag.
func (s *Service) UpdateAllowMention(id uint, allow bool) error {
	return s.db.Model(&Container{}).Where("id = ?", id).Update("allow_mention", allow).Error
}

// TouchActivity updates the container's last active time.
func (s *Service) TouchActivity(id uint) error {
	now := time.Now()
	return s.db.Model(&Container{}).Where("id = ?", id).Update("last_active_at", &now).Error
}

// EnsureRunning wakes up a sleeping container if needed.
func (s *Service) EnsureRunning(c *Container) error {
	if c.Status != "sleeping" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.mgr.StartContainer(ctx, c.ContainerID); err != nil {
		return fmt.Errorf("failed to wake up container: %w", err)
	}
	_ = s.UpdateStatus(c.ID, "active")
	time.Sleep(3 * time.Second) // Give container time to start
	return nil
}

// RestartContainer restarts a container's Docker instance (recovers crashed gateway).
func (s *Service) RestartContainer(c *Container) error {
	if c.ContainerID == "" {
		return fmt.Errorf("container has no Docker instance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.mgr.RestartContainer(ctx, c.ContainerID); err != nil {
		return fmt.Errorf("failed to restart container: %w", err)
	}
	_ = s.UpdateStatus(c.ID, "active")
	_ = s.TouchActivity(c.ID)
	return nil
}

// ApplyChanges regenerates openclaw.json and restarts the container.
func (s *Service) ApplyChanges(c *Container) error {
	extras := s.buildExtras(c.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := s.mgr.RegenerateConfig(ctx, c.ContainerName, c.ContainerID, c.GatewayToken, &s.cfg.OpenClaw, extras); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}
	return nil
}

// LogMessage records a message in the message log.
func (s *Service) LogMessage(containerID uint, direction, msgType, content string) error {
	log := &MessageLog{
		ContainerID: containerID,
		Direction:   direction,
		MsgType:     msgType,
		Content:     content,
		Status:      "pending",
	}
	return s.db.Create(log).Error
}

// GetMessageHistory returns recent messages for a container.
func (s *Service) GetMessageHistory(containerID uint, limit int) ([]MessageLog, error) {
	var logs []MessageLog
	if limit <= 0 {
		limit = 50
	}
	result := s.db.Where("container_id = ?", containerID).
		Order("created_at DESC").Limit(limit).Find(&logs)
	if result.Error != nil {
		return nil, result.Error
	}
	// Reverse to chronological order
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}
	return logs, nil
}

// GetIdleContainers returns active containers idle for longer than the specified duration.
func (s *Service) GetIdleContainers(idleMinutes int) ([]Container, error) {
	threshold := time.Now().Add(-time.Duration(idleMinutes) * time.Minute)
	var containers []Container
	result := s.db.Where("status = ? AND last_active_at < ?", "active", threshold).Find(&containers)
	if result.Error != nil {
		return nil, result.Error
	}
	return containers, nil
}

// IsContainerRunning checks if a container's Docker instance is running.
func (s *Service) IsContainerRunning(c *Container) (bool, error) {
	if c.ContainerID == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.mgr.IsContainerRunning(ctx, c.ContainerID)
}

func (s *Service) buildExtras(containerID uint) *OpenClawExtras {
	skills, skillDirs, mcps, err := s.catalogService.BuildOpenClawExtras(containerID)
	if err != nil {
		logger.Warn("Failed to build OpenClaw extras", "container_id", containerID, "error", err)
		return nil
	}
	if len(skills) == 0 && len(mcps) == 0 {
		return nil
	}
	return &OpenClawExtras{
		Skills:    skills,
		SkillDirs: skillDirs,
		MCPs:      mcps,
	}
}
