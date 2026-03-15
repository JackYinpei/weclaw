package groupchat

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Service provides business-layer operations for group chat rooms.
type Service struct {
	db *gorm.DB
}

// NewService creates a new group chat service.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// CreateRoom creates a new chat room.
func (s *Service) CreateRoom(name string, createdByID uint) (*ChatRoom, error) {
	room := &ChatRoom{
		Name:        name,
		CreatedByID: createdByID,
	}
	if err := s.db.Create(room).Error; err != nil {
		return nil, fmt.Errorf("failed to create room: %w", err)
	}
	return room, nil
}

// ListRooms returns all rooms that a user is a member of.
func (s *Service) ListRooms(accountID uint) ([]ChatRoom, error) {
	var rooms []ChatRoom
	err := s.db.Where("id IN (?)",
		s.db.Model(&ChatRoomMember{}).Select("room_id").Where("account_id = ?", accountID),
	).Order("updated_at DESC").Find(&rooms).Error
	return rooms, err
}

// GetRoom returns a room by ID (no ownership check—membership checked separately).
func (s *Service) GetRoom(roomID uint) (*ChatRoom, error) {
	var room ChatRoom
	if err := s.db.First(&room, roomID).Error; err != nil {
		return nil, err
	}
	return &room, nil
}

// DeleteRoom deletes a room and all its members/messages. Only the creator can delete.
func (s *Service) DeleteRoom(roomID, accountID uint) error {
	var room ChatRoom
	if err := s.db.First(&room, roomID).Error; err != nil {
		return fmt.Errorf("room not found: %w", err)
	}
	if room.CreatedByID != accountID {
		return fmt.Errorf("only room creator can delete")
	}
	s.db.Where("room_id = ?", roomID).Delete(&ChatRoomMember{})
	s.db.Where("room_id = ?", roomID).Delete(&ChatRoomMessage{})
	return s.db.Unscoped().Delete(&room).Error
}

// IsMember checks if an account is a member of a room.
func (s *Service) IsMember(roomID, accountID uint) bool {
	var count int64
	s.db.Model(&ChatRoomMember{}).Where("room_id = ? AND account_id = ?", roomID, accountID).Count(&count)
	return count > 0
}

// JoinRoom adds a user with their selected container to a room.
func (s *Service) JoinRoom(roomID, accountID, containerID uint) (*ChatRoomMember, error) {
	// Check not already in room
	if s.IsMember(roomID, accountID) {
		return nil, fmt.Errorf("already a member of this room")
	}
	member := &ChatRoomMember{
		RoomID:      roomID,
		AccountID:   accountID,
		ContainerID: containerID,
		JoinedAt:    time.Now(),
	}
	if err := s.db.Create(member).Error; err != nil {
		return nil, fmt.Errorf("failed to join room: %w", err)
	}
	return member, nil
}

// LeaveRoom removes a user from a room.
func (s *Service) LeaveRoom(roomID, accountID uint) error {
	result := s.db.Where("room_id = ? AND account_id = ?", roomID, accountID).Delete(&ChatRoomMember{})
	if result.RowsAffected == 0 {
		return fmt.Errorf("not a member of this room")
	}
	return result.Error
}

// GetMembers returns all members of a room with their account info.
func (s *Service) GetMembers(roomID uint) ([]ChatRoomMember, error) {
	var members []ChatRoomMember
	err := s.db.Where("room_id = ?", roomID).Find(&members).Error
	return members, err
}

// SaveMessage persists a group chat message.
func (s *Service) SaveMessage(msg *ChatRoomMessage) error {
	return s.db.Create(msg).Error
}

// GetMessages returns recent messages for a room.
func (s *Service) GetMessages(roomID uint, limit int) ([]ChatRoomMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	var msgs []ChatRoomMessage
	err := s.db.Where("room_id = ?", roomID).
		Order("created_at DESC").Limit(limit).Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	// Reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

// GetMemberByAccountID returns the member record for a specific account in a room.
func (s *Service) GetMemberByAccountID(roomID, accountID uint) (*ChatRoomMember, error) {
	var m ChatRoomMember
	err := s.db.Where("room_id = ? AND account_id = ?", roomID, accountID).First(&m).Error
	if err != nil {
		return nil, err
	}
	return &m, nil
}
