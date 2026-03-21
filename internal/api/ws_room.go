package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/internal/groupchat"
	"github.com/qcy/weclaw/pkg/logger"
)

// --- Room WebSocket message types ---

type roomWSIncoming struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

type roomWSOutgoing struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id,omitempty"`
	AccountID   uint   `json:"account_id,omitempty"`
	ContainerID uint   `json:"container_id,omitempty"`
	SenderType  string `json:"sender_type,omitempty"` // "user" or "agent"
	SenderName  string `json:"sender_name,omitempty"`
	Text        string `json:"text,omitempty"`
	Error       string `json:"error,omitempty"`
	// For member list updates
	Members []roomMemberInfo `json:"members,omitempty"`
}

type roomMemberInfo struct {
	AccountID     uint   `json:"account_id"`
	Username      string `json:"username"`
	ContainerID   uint   `json:"container_id"`
	ContainerName string `json:"container_name"`
	AllowMention  bool   `json:"allow_mention"`
}

// --- Room Connection Hub ---

type roomHub struct {
	mu    sync.Mutex
	conns map[uint][]*roomConn // roomID -> connections
}

var rHub = &roomHub{conns: make(map[uint][]*roomConn)}

func (h *roomHub) add(roomID uint, c *roomConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[roomID] = append(h.conns[roomID], c)
}

func (h *roomHub) remove(roomID uint, c *roomConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := h.conns[roomID]
	for i, conn := range conns {
		if conn == c {
			h.conns[roomID] = append(conns[:i], conns[i+1:]...)
			break
		}
	}
	if len(h.conns[roomID]) == 0 {
		delete(h.conns, roomID)
	}
}

func (h *roomHub) broadcast(roomID uint, msg roomWSOutgoing) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	conns := make([]*roomConn, len(h.conns[roomID]))
	copy(conns, h.conns[roomID])
	h.mu.Unlock()

	for _, c := range conns {
		select {
		case c.send <- data:
		default:
		}
	}
}

// --- Room WebSocket Connection ---

type roomConn struct {
	conn      *websocket.Conn
	send      chan []byte
	api       *ContainerAPI
	roomID    uint
	accountID uint
	username  string
	ctx       context.Context
	cancel    context.CancelFunc

	// Per-agent session tracking: containerID -> lastResponseID
	agentSessions sync.Map
	// Per-agent in-flight guard: containerID -> bool
	agentInFlight sync.Map
}

func (rc *roomConn) sendJSON(msg roomWSOutgoing) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case rc.send <- data:
	default:
	}
}

// --- Room WebSocket Handler ---

func (api *ContainerAPI) HandleRoomWebSocket(c *gin.Context) {
	tokenStr := c.Query("token")
	if tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "token required"})
		return
	}

	claims, err := parseJWT(tokenStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}

	var accountID uint
	switch v := claims["sub"].(type) {
	case float64:
		accountID = uint(v)
	default:
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		return
	}
	username, _ := claims["username"].(string)

	roomIDStr := c.Param("roomId")
	roomID, err := strconv.ParseUint(roomIDStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room id"})
		return
	}

	// Verify membership
	if !api.groupChatService.IsMember(uint(roomID), accountID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "not a member of this room"})
		return
	}

	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Error("Room WebSocket upgrade failed", "error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	rc := &roomConn{
		conn:      ws,
		send:      make(chan []byte, 256),
		api:       api,
		roomID:    uint(roomID),
		accountID: accountID,
		username:  username,
		ctx:       ctx,
		cancel:    cancel,
	}

	rHub.add(rc.roomID, rc)

	// Send connected + member list
	rc.sendJSON(roomWSOutgoing{Type: "connected"})
	api.broadcastMemberList(rc.roomID)

	go rc.roomWritePump()
	go rc.roomReadPump()
}

func (rc *roomConn) roomReadPump() {
	defer func() {
		rc.cancel()
		rHub.remove(rc.roomID, rc)
		rc.conn.Close()
		rc.api.broadcastMemberList(rc.roomID)
	}()

	rc.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	rc.conn.SetPongHandler(func(string) error {
		rc.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := rc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.Debug("Room WS read error", "room_id", rc.roomID, "error", err)
			}
			return
		}
		rc.conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		var msg roomWSIncoming
		if err := json.Unmarshal(message, &msg); err != nil {
			rc.sendJSON(roomWSOutgoing{Type: "error", Error: "invalid message format"})
			continue
		}

		switch msg.Type {
		case "send_message":
			rc.handleRoomMessage(msg.Message)
		case "pong":
			// no-op
		default:
			rc.sendJSON(roomWSOutgoing{Type: "error", Error: "unknown message type"})
		}
	}
}

func (rc *roomConn) roomWritePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		rc.conn.Close()
	}()

	for {
		select {
		case <-rc.ctx.Done():
			rc.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case msg, ok := <-rc.send:
			if !ok {
				return
			}
			rc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := rc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			rc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := rc.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleRoomMessage processes a message in the group chat.
// If it contains @AgentName, dispatch to the target agent's container.
func (rc *roomConn) handleRoomMessage(message string) {
	if message == "" {
		rc.sendJSON(roomWSOutgoing{Type: "error", Error: "empty message"})
		return
	}

	// Broadcast user message to all room members
	userMsg := roomWSOutgoing{
		Type:       "room_message",
		AccountID:  rc.accountID,
		SenderType: "user",
		SenderName: rc.username,
		Text:       message,
	}
	rHub.broadcast(rc.roomID, userMsg)

	// Persist user message
	_ = rc.api.groupChatService.SaveMessage(&groupchat.ChatRoomMessage{
		RoomID:     rc.roomID,
		AccountID:  rc.accountID,
		SenderType: "user",
		SenderName: rc.username,
		Content:    message,
	})

	// Parse @ mentions and dispatch to agents
	rc.dispatchMentions(message)
}

// dispatchMentions finds @AgentName patterns and sends messages to the corresponding containers.
func (rc *roomConn) dispatchMentions(message string) {
	members, err := rc.api.groupChatService.GetMembers(rc.roomID)
	if err != nil {
		return
	}

	// Build a map of container display names to member info
	type agentTarget struct {
		member    groupchat.ChatRoomMember
		container *container.Container
	}

	// Load all member containers to match @mentions
	var targets []agentTarget
	for _, m := range members {
		// Use GetByIDNoOwnerCheck since we need cross-account container lookup
		ctr, err := rc.api.containerService.GetByIDNoOwnerCheck(m.ContainerID)
		if err != nil {
			continue
		}

		// Check if this agent name is mentioned
		agentName := ctr.DisplayName
		if agentName == "" {
			agentName = fmt.Sprintf("Agent-%d", ctr.ID)
		}

		mention := "@" + agentName
		if !strings.Contains(message, mention) {
			continue
		}

		// Permission check: if not owner, check AllowMention
		if m.AccountID != rc.accountID && !ctr.AllowMention {
			rc.sendJSON(roomWSOutgoing{
				Type:  "error",
				Error: fmt.Sprintf("%s does not allow mentions from other users", agentName),
			})
			continue
		}

		targets = append(targets, agentTarget{member: m, container: ctr})
	}

	// Dispatch to each mentioned agent
	for _, t := range targets {
		go rc.streamToAgent(t.container, t.member, message)
	}
}

// buildRoomContext fetches recent room messages and formats them as context for the agent.
// This allows each agent to see what other users and agents have said in the group chat.
func (rc *roomConn) buildRoomContext(ctr *container.Container, agentName, message string) string {
	msgs, err := rc.api.groupChatService.GetMessages(rc.roomID, 30)
	if err != nil || len(msgs) == 0 {
		return message
	}

	room, _ := rc.api.groupChatService.GetRoom(rc.roomID)
	roomName := "Group Chat"
	if room != nil && room.Name != "" {
		roomName = room.Name
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[群聊房间 \"%s\" 的对话上下文 — 你的身份: %s]\n", roomName, agentName))
	sb.WriteString("以下是最近的对话记录，请基于这些上下文来理解和回复最新消息：\n\n")

	for _, m := range msgs {
		label := m.SenderName
		if m.SenderType == "agent" {
			label += " (Agent)"
		}
		// Truncate very long messages in context to save tokens
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("%s: %s\n", label, content))
	}

	sb.WriteString(fmt.Sprintf("\n[最新消息 — 来自 %s]\n%s", rc.username, message))
	return sb.String()
}

// streamToAgent sends a message to an agent's container and broadcasts the streamed response.
func (rc *roomConn) streamToAgent(ctr *container.Container, member groupchat.ChatRoomMember, message string) {
	// In-flight guard per container (local to this roomConn)
	key := ctr.ID
	if _, loaded := rc.agentInFlight.LoadOrStore(key, true); loaded {
		rc.sendJSON(roomWSOutgoing{
			Type:  "error",
			Error: fmt.Sprintf("%s is busy processing another message", ctr.DisplayName),
		})
		return
	}
	defer rc.agentInFlight.Delete(key)

	// Global per-container stream guard: prevent concurrent SSE streams
	// (e.g. private chat already streaming to this container)
	if !acquireContainerStream(ctr.ID, fmt.Sprintf("room%d-acct%d", rc.roomID, member.AccountID)) {
		rHub.broadcast(rc.roomID, roomWSOutgoing{
			Type:  "error",
			Error: fmt.Sprintf("%s 正在处理其他消息，请稍后再试", ctr.DisplayName),
		})
		return
	}
	defer releaseContainerStream(ctr.ID)

	if ctr.ContainerID == "" || ctr.ContainerPort == 0 {
		rHub.broadcast(rc.roomID, roomWSOutgoing{
			Type:  "error",
			Error: fmt.Sprintf("%s has no running Docker instance", ctr.DisplayName),
		})
		return
	}

	// Ensure container is running
	if err := rc.api.containerService.EnsureRunning(ctr); err != nil {
		rHub.broadcast(rc.roomID, roomWSOutgoing{
			Type:  "error",
			Error: fmt.Sprintf("Failed to wake %s: %v", ctr.DisplayName, err),
		})
		return
	}

	agentName := ctr.DisplayName
	if agentName == "" {
		agentName = fmt.Sprintf("Agent-%d", ctr.ID)
	}

	reqID := fmt.Sprintf("room-%d-ctr%d-%d", rc.roomID, ctr.ID, time.Now().UnixMilli())

	// Broadcast stream start
	rHub.broadcast(rc.roomID, roomWSOutgoing{
		Type:        "room_stream_start",
		RequestID:   reqID,
		ContainerID: ctr.ID,
		SenderType:  "agent",
		SenderName:  agentName,
	})

	// Use room-specific session key so group chat has separate context from private chat
	userID := fmt.Sprintf("weclaw-room%d-acct%d-ctr%d", rc.roomID, member.AccountID, ctr.ID)

	// Get previous response ID for this agent in this room
	var prevRespID string
	if v, ok := rc.agentSessions.Load(key); ok {
		prevRespID, _ = v.(string)
	}

	// Build context-enriched message so the agent can see the full group chat conversation
	contextMessage := rc.buildRoomContext(ctr, agentName, message)

	ch, err := rc.api.openclawClient.StreamMessage(rc.ctx, ctr.ContainerPort, ctr.GatewayToken, contextMessage, userID, prevRespID)
	if err != nil {
		rHub.broadcast(rc.roomID, roomWSOutgoing{
			Type:      "room_stream_error",
			RequestID: reqID,
			Error:     err.Error(),
		})
		return
	}

	var fullText string
	var gotDone bool
	for evt := range ch {
		switch evt.Type {
		case "text_delta":
			rHub.broadcast(rc.roomID, roomWSOutgoing{
				Type:        "room_stream_delta",
				RequestID:   reqID,
				ContainerID: ctr.ID,
				SenderType:  "agent",
				SenderName:  agentName,
				Text:        evt.Text,
			})
			fullText += evt.Text
		case "text_done":
			fullText = evt.Text
			gotDone = true
			rHub.broadcast(rc.roomID, roomWSOutgoing{
				Type:        "room_stream_done",
				RequestID:   reqID,
				ContainerID: ctr.ID,
				SenderType:  "agent",
				SenderName:  agentName,
				Text:        evt.Text,
			})
		case "completed":
			if evt.ResponseID != "" {
				rc.agentSessions.Store(key, evt.ResponseID)
			}
			if !gotDone && fullText != "" {
				rHub.broadcast(rc.roomID, roomWSOutgoing{
					Type:        "room_stream_done",
					RequestID:   reqID,
					ContainerID: ctr.ID,
					SenderType:  "agent",
					SenderName:  agentName,
					Text:        fullText,
				})
			}
		case "error":
			rHub.broadcast(rc.roomID, roomWSOutgoing{
				Type:      "room_stream_error",
				RequestID: reqID,
				Error:     evt.Text,
			})
			return
		}
	}

	// Persist agent response
	if fullText != "" {
		_ = rc.api.groupChatService.SaveMessage(&groupchat.ChatRoomMessage{
			RoomID:      rc.roomID,
			AccountID:   member.AccountID,
			ContainerID: ctr.ID,
			SenderType:  "agent",
			SenderName:  agentName,
			Content:     fullText,
		})
		_ = rc.api.containerService.TouchActivity(ctr.ID)
	}
}

// broadcastMemberList sends the current member list to all connections in a room.
func (api *ContainerAPI) broadcastMemberList(roomID uint) {
	members, err := api.groupChatService.GetMembers(roomID)
	if err != nil {
		return
	}

	var infos []roomMemberInfo
	for _, m := range members {
		username := api.getUsernameByID(m.AccountID)
		ctr, err := api.containerService.GetByIDNoOwnerCheck(m.ContainerID)
		if err != nil {
			// Still include the member even if container is not found (e.g., soft-deleted)
			infos = append(infos, roomMemberInfo{
				AccountID:     m.AccountID,
				Username:      username,
				ContainerID:   m.ContainerID,
				ContainerName: "(offline)",
				AllowMention:  false,
			})
			continue
		}
		ctrName := ctr.DisplayName
		if ctrName == "" {
			ctrName = fmt.Sprintf("Agent-%d", ctr.ID)
		}
		infos = append(infos, roomMemberInfo{
			AccountID:     m.AccountID,
			Username:      username,
			ContainerID:   m.ContainerID,
			ContainerName: ctrName,
			AllowMention:  ctr.AllowMention,
		})
	}

	rHub.broadcast(roomID, roomWSOutgoing{
		Type:    "member_list",
		Members: infos,
	})
}
