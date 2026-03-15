package api

import (
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

	// Reset lastResponseID on all active WebSocket connections for this container.
	// This makes the next message start a fresh OpenClaw session.
	hub.mu.Lock()
	conns := hub.conns[ctr.ID]
	hub.mu.Unlock()
	for _, wc := range conns {
		wc.mu.Lock()
		wc.lastResponseID = ""
		wc.mu.Unlock()
	}

	c.JSON(http.StatusOK, gin.H{"message": "new session started"})
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
