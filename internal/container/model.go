package container

import (
	"time"

	"gorm.io/gorm"
)

// Container represents a user-owned OpenClaw container instance.
type Container struct {
	ID            uint           `gorm:"primarykey" json:"id"`
	AccountID     uint           `gorm:"index;not null" json:"account_id"`
	ContainerID   string         `gorm:"size:128" json:"container_id"`
	ContainerName string         `gorm:"size:128" json:"container_name"`
	ContainerPort int            `json:"container_port"`
	GatewayToken  string         `gorm:"size:256" json:"gateway_token"`
	DisplayName   string         `gorm:"size:128" json:"display_name"`
	Status        string         `gorm:"size:20;default:pending" json:"status"` // pending/active/sleeping/disabled
	LastActiveAt  *time.Time     `json:"last_active_at"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

// MessageLog records messages between user and OpenClaw for a specific container.
type MessageLog struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	ContainerID   uint      `gorm:"index;not null" json:"container_id"`
	Direction     string    `gorm:"size:10;not null" json:"direction"` // "incoming" or "outgoing"
	MsgType       string    `gorm:"size:20;not null" json:"msg_type"`
	Content       string    `gorm:"type:text" json:"content"`
	OpenClawReqID string    `gorm:"size:128" json:"openclaw_req_id"`
	Status        string    `gorm:"size:20;default:pending" json:"status"` // pending/processing/done/failed
	CreatedAt     time.Time `json:"created_at"`
}
