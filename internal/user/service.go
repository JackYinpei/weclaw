package user

import (
	"fmt"
	"time"

	"github.com/qcy/weclaw/internal/config"
	"github.com/qcy/weclaw/pkg/logger"
	"gorm.io/gorm"
)

// Service provides user management operations.
type Service struct {
	db       *gorm.DB
	quotaCfg *config.QuotaConfig
}

// NewService creates a new user service.
func NewService(db *gorm.DB, quotaCfg *config.QuotaConfig) *Service {
	return &Service{
		db:       db,
		quotaCfg: quotaCfg,
	}
}

// FindByOpenID finds a user by their WeChat OpenID.
func (s *Service) FindByOpenID(openID string) (*User, error) {
	var u User
	result := s.db.Where("open_id = ?", openID).First(&u)
	if result.Error != nil {
		return nil, result.Error
	}
	return &u, nil
}

// Create creates a new user record.
func (s *Service) Create(openID string) (*User, error) {
	u := &User{
		OpenID: openID,
		Status: StatusPending,
	}
	result := s.db.Create(u)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to create user: %w", result.Error)
	}
	logger.Info("User created", "openid", openID, "id", u.ID)
	return u, nil
}

// UpdateContainerInfo updates the container information for a user.
func (s *Service) UpdateContainerInfo(openID, containerID, containerName string, port int, gatewayToken string) error {
	result := s.db.Model(&User{}).Where("open_id = ?", openID).Updates(map[string]interface{}{
		"container_id":   containerID,
		"container_name": containerName,
		"container_port": port,
		"gateway_token":  gatewayToken,
		"status":         StatusActive,
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update container info: %w", result.Error)
	}
	return nil
}

// UpdateStatus updates the user's status.
func (s *Service) UpdateStatus(openID string, status Status) error {
	result := s.db.Model(&User{}).Where("open_id = ?", openID).Update("status", status)
	if result.Error != nil {
		return fmt.Errorf("failed to update status: %w", result.Error)
	}
	return nil
}

// TouchActivity updates the user's last active time.
func (s *Service) TouchActivity(openID string) error {
	now := time.Now()
	result := s.db.Model(&User{}).Where("open_id = ?", openID).Update("last_active_at", &now)
	return result.Error
}

// IncrementMsgCount increments the user's daily message count.
func (s *Service) IncrementMsgCount(openID string) error {
	result := s.db.Model(&User{}).Where("open_id = ?", openID).
		Update("msg_count_today", gorm.Expr("msg_count_today + 1"))
	return result.Error
}

// CheckQuota checks if the user has exceeded their daily message quota.
func (s *Service) CheckQuota(openID string) (bool, error) {
	var u User
	result := s.db.Where("open_id = ?", openID).First(&u)
	if result.Error != nil {
		return false, result.Error
	}
	return u.MsgCountToday < s.quotaCfg.MaxMessagesPerDay, nil
}

// ResetDailyQuotas resets the daily message count for all users.
// Should be called by a daily cron job at midnight.
func (s *Service) ResetDailyQuotas() error {
	result := s.db.Model(&User{}).Where("msg_count_today > 0").Update("msg_count_today", 0)
	if result.Error != nil {
		return fmt.Errorf("failed to reset daily quotas: %w", result.Error)
	}
	logger.Info("Daily quotas reset", "affected_users", result.RowsAffected)
	return nil
}

// GetIdleUsers returns users whose containers have been idle for longer than the specified duration.
func (s *Service) GetIdleUsers(idleMinutes int) ([]User, error) {
	threshold := time.Now().Add(-time.Duration(idleMinutes) * time.Minute)
	var users []User
	result := s.db.Where("status = ? AND last_active_at < ?", StatusActive, threshold).Find(&users)
	if result.Error != nil {
		return nil, result.Error
	}
	return users, nil
}

// LogMessage records a message in the message log.
func (s *Service) LogMessage(userID uint, direction, msgType, content string) error {
	log := &MessageLog{
		UserID:    userID,
		Direction: direction,
		MsgType:   msgType,
		Content:   content,
		Status:    "pending",
	}
	result := s.db.Create(log)
	return result.Error
}

// GetMessageHistory returns recent messages for a user, ordered by time ascending.
func (s *Service) GetMessageHistory(userID uint, limit int) ([]MessageLog, error) {
	var logs []MessageLog
	if limit <= 0 {
		limit = 50
	}
	// Subquery: get the last N messages (desc), then reverse to asc order
	result := s.db.Where("user_id = ?", userID).
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

// Disable marks a user as disabled (e.g., when they unsubscribe).
func (s *Service) Disable(openID string) error {
	return s.UpdateStatus(openID, StatusDisabled)
}

// ListAll returns all users (for testing/admin purposes).
func (s *Service) ListAll() ([]User, error) {
	var users []User
	result := s.db.Find(&users)
	if result.Error != nil {
		return nil, result.Error
	}
	return users, nil
}

// Delete permanently removes a user record (for testing purposes).
func (s *Service) Delete(openID string) error {
	// Hard delete (bypass soft delete)
	result := s.db.Unscoped().Where("open_id = ?", openID).Delete(&User{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete user: %w", result.Error)
	}
	logger.Info("User deleted", "openid", openID)
	return nil
}
