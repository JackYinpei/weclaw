package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/qcy/weclaw/internal/container"
	"github.com/qcy/weclaw/pkg/logger"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// --- WebSocket message types ---

type wsIncoming struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

type wsOutgoing struct {
	Type        string `json:"type"`
	ContainerID uint   `json:"container_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	Text        string `json:"text,omitempty"`
	Ready       *bool  `json:"ready,omitempty"`
	Error       string `json:"error,omitempty"`
}

// --- Connection Hub ---

type connHub struct {
	mu    sync.Mutex
	conns map[uint][]*wsConn // containerID -> connections
}

var hub = &connHub{conns: make(map[uint][]*wsConn)}

func (h *connHub) add(containerID uint, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[containerID] = append(h.conns[containerID], c)
}

func (h *connHub) remove(containerID uint, c *wsConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := h.conns[containerID]
	for i, conn := range conns {
		if conn == c {
			h.conns[containerID] = append(conns[:i], conns[i+1:]...)
			break
		}
	}
	if len(h.conns[containerID]) == 0 {
		delete(h.conns, containerID)
	}
}

// --- WebSocket Connection ---

type wsConn struct {
	conn      *websocket.Conn
	send      chan []byte
	api       *ContainerAPI
	container *container.Container
	ctx       context.Context
	cancel    context.CancelFunc
	inFlight  bool
	mu        sync.Mutex // guards inFlight
}

func (wc *wsConn) sendJSON(msg wsOutgoing) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case wc.send <- data:
	default:
		// Channel full, drop message
	}
}

// --- WebSocket Handler ---

// HandleWebSocket handles WebSocket connections for container streaming.
func (api *ContainerAPI) HandleWebSocket(c *gin.Context) {
	// JWT from query param (WS handshake can't send headers)
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

	// Extract accountID from claims
	var accountID uint
	switch v := claims["sub"].(type) {
	case float64:
		accountID = uint(v)
	default:
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		return
	}

	// Resolve container
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid container id"})
		return
	}

	ctr, err := api.containerService.GetByID(uint(id), accountID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "container not found"})
		return
	}

	// Upgrade to WebSocket
	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed", "error", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	wc := &wsConn{
		conn:      ws,
		send:      make(chan []byte, 256),
		api:       api,
		container: ctr,
		ctx:       ctx,
		cancel:    cancel,
	}

	hub.add(ctr.ID, wc)

	// Send connected message
	wc.sendJSON(wsOutgoing{Type: "connected", ContainerID: ctr.ID})

	// Start pumps
	go wc.writePump()
	go wc.gatewayStatusPump()
	go wc.readPump()
}

// readPump reads messages from the WebSocket and dispatches them.
func (wc *wsConn) readPump() {
	defer func() {
		wc.cancel()
		hub.remove(wc.container.ID, wc)
		wc.conn.Close()
	}()

	wc.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	wc.conn.SetPongHandler(func(string) error {
		wc.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := wc.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				logger.Debug("WebSocket read error", "container_id", wc.container.ID, "error", err)
			}
			return
		}

		// Reset read deadline on any message
		wc.conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		var msg wsIncoming
		if err := json.Unmarshal(message, &msg); err != nil {
			wc.sendJSON(wsOutgoing{Type: "error", Error: "invalid message format"})
			continue
		}

		switch msg.Type {
		case "send_message":
			wc.handleSendMessage(msg.Message)
		case "ping_gateway":
			wc.handlePingGateway()
		case "pong":
			// No-op, client responding to our ping
		default:
			wc.sendJSON(wsOutgoing{Type: "error", Error: "unknown message type"})
		}
	}
}

// writePump writes messages from the send channel to the WebSocket.
// This is the ONLY goroutine that writes to the WebSocket (prevents concurrent write panics).
func (wc *wsConn) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		wc.conn.Close()
	}()

	for {
		select {
		case <-wc.ctx.Done():
			wc.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return

		case msg, ok := <-wc.send:
			if !ok {
				return
			}
			wc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := wc.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}

		case <-ticker.C:
			wc.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := wc.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// gatewayStatusPump checks gateway health and pushes status updates.
func (wc *wsConn) gatewayStatusPump() {
	if wc.container.ContainerPort == 0 || wc.container.GatewayToken == "" {
		wc.sendJSON(wsOutgoing{Type: "gateway_status", Ready: boolPtr(false)})
		return
	}

	var lastReady bool

	// Check immediately
	ready := wc.api.openclawClient.CheckHealth(wc.container.ContainerPort, wc.container.GatewayToken)
	lastReady = ready
	wc.sendJSON(wsOutgoing{Type: "gateway_status", Ready: boolPtr(ready)})

	// Poll interval: 3s when not ready, 30s when ready
	interval := 3 * time.Second
	if ready {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-wc.ctx.Done():
			return
		case <-ticker.C:
			ready := wc.api.openclawClient.CheckHealth(wc.container.ContainerPort, wc.container.GatewayToken)
			if ready != lastReady {
				wc.sendJSON(wsOutgoing{Type: "gateway_status", Ready: boolPtr(ready)})
				lastReady = ready
			}
			// Adjust poll interval
			newInterval := 3 * time.Second
			if ready {
				newInterval = 30 * time.Second
			}
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
			}
		}
	}
}

// handleSendMessage processes an incoming send_message request.
func (wc *wsConn) handleSendMessage(message string) {
	if message == "" {
		wc.sendJSON(wsOutgoing{Type: "error", Error: "empty message"})
		return
	}

	// Concurrency guard: only one message in flight at a time
	wc.mu.Lock()
	if wc.inFlight {
		wc.mu.Unlock()
		wc.sendJSON(wsOutgoing{Type: "error", Error: "message_in_flight"})
		return
	}
	wc.inFlight = true
	wc.mu.Unlock()

	go func() {
		defer func() {
			wc.mu.Lock()
			wc.inFlight = false
			wc.mu.Unlock()
		}()

		ctr := wc.container

		if ctr.ContainerID == "" || ctr.ContainerPort == 0 {
			wc.sendJSON(wsOutgoing{Type: "error", Error: "container has no Docker instance"})
			return
		}

		// Ensure container is running (wake if sleeping)
		if err := wc.api.containerService.EnsureRunning(ctr); err != nil {
			wc.sendJSON(wsOutgoing{Type: "error", Error: fmt.Sprintf("failed to wake container: %v", err)})
			return
		}

		// Log incoming message
		_ = wc.api.containerService.LogMessage(ctr.ID, "incoming", "text", message)

		reqID := fmt.Sprintf("req-%d", time.Now().UnixMilli())
		wc.sendJSON(wsOutgoing{Type: "stream_start", RequestID: reqID})

		// Stream SSE from gateway
		ch, err := wc.api.openclawClient.StreamMessage(wc.ctx, ctr.ContainerPort, ctr.GatewayToken, message)
		if err != nil {
			wc.sendJSON(wsOutgoing{Type: "stream_error", RequestID: reqID, Error: err.Error()})
			return
		}

		var fullText string
		var gotDone bool
		for evt := range ch {
			switch evt.Type {
			case "text_delta":
				wc.sendJSON(wsOutgoing{Type: "stream_delta", RequestID: reqID, Text: evt.Text})
				fullText += evt.Text
			case "text_done":
				fullText = evt.Text
				gotDone = true
				wc.sendJSON(wsOutgoing{Type: "stream_done", RequestID: reqID, Text: evt.Text})
			case "completed":
				// If we got deltas but no text_done, send stream_done with accumulated text
				if !gotDone && fullText != "" {
					wc.sendJSON(wsOutgoing{Type: "stream_done", RequestID: reqID, Text: fullText})
				}
			case "error":
				wc.sendJSON(wsOutgoing{Type: "stream_error", RequestID: reqID, Error: evt.Text})
				return
			}
		}

		// Log outgoing response
		if fullText != "" {
			// Log outgoing response
			_ = wc.api.containerService.LogMessage(ctr.ID, "outgoing", "text", fullText)
			_ = wc.api.containerService.TouchActivity(ctr.ID)
		}
	}()
}

// handlePingGateway responds with the current gateway health status.
func (wc *wsConn) handlePingGateway() {
	ready := false
	if wc.container.ContainerPort > 0 && wc.container.GatewayToken != "" {
		ready = wc.api.openclawClient.CheckHealth(wc.container.ContainerPort, wc.container.GatewayToken)
	}
	wc.sendJSON(wsOutgoing{Type: "gateway_status", Ready: boolPtr(ready)})
}

func boolPtr(b bool) *bool { return &b }
