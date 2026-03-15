package groupchat

import (
	"time"

	"gorm.io/gorm"
)

// ChatRoom represents a group chat room where multiple users and their agents interact.
type ChatRoom struct {
	ID          uint           `gorm:"primarykey" json:"id"`
	Name        string         `gorm:"size:128;not null" json:"name"`
	CreatedByID uint           `gorm:"not null" json:"created_by_id"` // Account ID of creator
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

// ChatRoomMember represents a user's membership in a chat room, linking their container (agent).
type ChatRoomMember struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	RoomID      uint      `gorm:"index;not null" json:"room_id"`
	AccountID   uint      `gorm:"index;not null" json:"account_id"`
	ContainerID uint      `gorm:"not null" json:"container_id"` // Which agent they bring into the room
	JoinedAt    time.Time `json:"joined_at"`
}

// ChatRoomMessage stores messages in a group chat room.
type ChatRoomMessage struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	RoomID      uint      `gorm:"index;not null" json:"room_id"`
	AccountID   uint      `gorm:"not null" json:"account_id"`      // Who sent it (or whose agent sent it)
	ContainerID uint      `json:"container_id"`                     // 0 for user messages, set for agent messages
	SenderType  string    `gorm:"size:10;not null" json:"sender_type"` // "user" or "agent"
	SenderName  string    `gorm:"size:128" json:"sender_name"`     // Display name (username or agent name)
	Content     string    `gorm:"type:text" json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}
