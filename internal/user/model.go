package user

import (
	"time"

	"gorm.io/gorm"
)

// Status represents the user's current status.
type Status string

const (
	StatusPending  Status = "pending"  // Container is being created
	StatusActive   Status = "active"   // Container is running
	StatusSleeping Status = "sleeping" // Container is stopped (idle)
	StatusDisabled Status = "disabled" // User unsubscribed
)

// User represents a WeChat user in the system.
type User struct {
	ID            uint           `gorm:"primarykey" json:"id"`
	OpenID        string         `gorm:"uniqueIndex;size:64;not null" json:"openid"`
	Nickname      string         `gorm:"size:128" json:"nickname"`
	ContainerID   string         `gorm:"size:128" json:"container_id"`
	ContainerName string         `gorm:"size:128" json:"container_name"`
	ContainerPort int            `json:"container_port"`
	GatewayToken  string         `gorm:"size:256" json:"gateway_token"`
	Status        Status         `gorm:"size:20;default:pending" json:"status"`
	MsgCountToday int            `gorm:"default:0" json:"msg_count_today"`
	LastActiveAt  *time.Time     `json:"last_active_at"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"deleted_at"`
}

// MessageLog records all messages between user and OpenClaw.
type MessageLog struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	UserID        uint      `gorm:"index;not null" json:"user_id"`
	Direction     string    `gorm:"size:10;not null" json:"direction"` // "incoming" or "outgoing"
	MsgType       string    `gorm:"size:20;not null" json:"msg_type"`
	Content       string    `gorm:"type:text" json:"content"`
	OpenClawReqID string    `gorm:"size:128" json:"openclaw_req_id"`
	Status        string    `gorm:"size:20;default:pending" json:"status"` // pending/processing/done/failed
	CreatedAt     time.Time `json:"created_at"`
}
