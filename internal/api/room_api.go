package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// --- Group Chat Room Handlers ---

func (api *ContainerAPI) ListRooms(c *gin.Context) {
	accountID := getAccountID(c)
	rooms, err := api.groupChatService.ListRooms(accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"rooms": rooms})
}

func (api *ContainerAPI) CreateRoom(c *gin.Context) {
	accountID := getAccountID(c)
	var req struct {
		Name        string `json:"name" binding:"required"`
		ContainerID uint   `json:"container_id" binding:"required"` // Which agent to bring
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify container ownership
	if _, err := api.containerService.GetByID(req.ContainerID, accountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "container not found or not owned by you"})
		return
	}

	room, err := api.groupChatService.CreateRoom(req.Name, accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Auto-join the creator
	_, err = api.groupChatService.JoinRoom(room.ID, accountID, req.ContainerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "room created but failed to join: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, room)
}

func (api *ContainerAPI) DeleteRoom(c *gin.Context) {
	accountID := getAccountID(c)
	roomID, err := parseRoomID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}

	if err := api.groupChatService.DeleteRoom(roomID, accountID); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "room deleted"})
}

func (api *ContainerAPI) JoinRoom(c *gin.Context) {
	accountID := getAccountID(c)
	roomID, err := parseRoomID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}

	var req struct {
		ContainerID uint `json:"container_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Verify container ownership
	if _, err := api.containerService.GetByID(req.ContainerID, accountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "container not found or not owned by you"})
		return
	}

	member, err := api.groupChatService.JoinRoom(roomID, accountID, req.ContainerID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Broadcast updated member list
	api.broadcastMemberList(roomID)

	c.JSON(http.StatusOK, member)
}

func (api *ContainerAPI) LeaveRoom(c *gin.Context) {
	accountID := getAccountID(c)
	roomID, err := parseRoomID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}

	if err := api.groupChatService.LeaveRoom(roomID, accountID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	api.broadcastMemberList(roomID)
	c.JSON(http.StatusOK, gin.H{"message": "left room"})
}

func (api *ContainerAPI) GetRoomMembers(c *gin.Context) {
	accountID := getAccountID(c)
	roomID, err := parseRoomID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}

	if !api.groupChatService.IsMember(roomID, accountID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return
	}

	members, err := api.groupChatService.GetMembers(roomID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": members})
}

func (api *ContainerAPI) GetRoomMessages(c *gin.Context) {
	accountID := getAccountID(c)
	roomID, err := parseRoomID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}

	if !api.groupChatService.IsMember(roomID, accountID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member"})
		return
	}

	limit := 50
	if l := c.Query("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	msgs, err := api.groupChatService.GetMessages(roomID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": msgs})
}

// --- Container AllowMention + New Session Handlers ---

func (api *ContainerAPI) UpdateAllowMention(c *gin.Context) {
	accountID := getAccountID(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctr, err := api.containerService.GetByID(uint(id), accountID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "container not found"})
		return
	}

	var req struct {
		AllowMention bool `json:"allow_mention"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := api.containerService.UpdateAllowMention(ctr.ID, req.AllowMention); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated", "allow_mention": req.AllowMention})
}

// NewSession resets the conversation session for a container's private chat by clearing
// the WebSocket connection's lastResponseID. This causes OpenClaw to start a new session.
func (api *ContainerAPI) NewSession(c *gin.Context) {
	accountID := getAccountID(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctr, err := api.containerService.GetByID(uint(id), accountID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "container not found"})
		return
	}

	// Reset lastResponseIDs on all active WebSocket connections for this container.
	// If session_tag is provided, only clear that session; otherwise clear all.
	var req struct {
		SessionTag string `json:"session_tag"`
	}
	_ = c.ShouldBindJSON(&req)

	hub.mu.Lock()
	conns := hub.conns[ctr.ID]
	hub.mu.Unlock()
	for _, wc := range conns {
		wc.mu.Lock()
		if req.SessionTag != "" {
			delete(wc.lastResponseIDs, req.SessionTag)
		} else {
			wc.lastResponseIDs = make(map[string]string)
		}
		wc.mu.Unlock()
	}

	c.JSON(http.StatusOK, gin.H{"message": "new session started"})
}

func (api *ContainerAPI) InviteToRoom(c *gin.Context) {
	accountID := getAccountID(c)
	roomID, err := parseRoomID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}

	// Verify caller is a member
	if !api.groupChatService.IsMember(roomID, accountID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member of this room"})
		return
	}

	var req struct {
		Username string `json:"username" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Find target user
	targetAcc, err := api.accountRepo.FindByUsername(context.Background(), req.Username)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Get target user's first container
	ctr, err := api.containerService.GetFirstActiveByAccount(targetAcc.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target user has no containers"})
		return
	}

	// Join room
	member, err := api.groupChatService.JoinRoom(roomID, targetAcc.ID, ctr.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Broadcast updated member list
	api.broadcastMemberList(roomID)

	c.JSON(http.StatusOK, gin.H{"message": "invited", "member": member})
}

// --- Helpers ---

func parseRoomID(c *gin.Context) (uint, error) {
	idStr := c.Param("roomId")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
