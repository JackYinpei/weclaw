package openclaw

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/qcy/weclaw/pkg/logger"
)

// StreamEvent represents a parsed SSE event from the OpenClaw Gateway /v1/responses endpoint.
type StreamEvent struct {
	Type string // "text_delta" | "text_done" | "completed" | "error"
	Text string // delta content / full text / error message
}

type responsesRequest struct {
	Model  string `json:"model"`
	Input  string `json:"input"`
	Stream bool   `json:"stream"`
}

// StreamMessage sends a message to the OpenClaw Gateway /v1/responses endpoint with streaming
// and returns a channel of StreamEvents. The channel is closed when the stream ends.
// If the gateway returns 404 (endpoint not enabled), it falls back to synchronous SendMessage.
func (c *Client) StreamMessage(ctx context.Context, port int, token, message string) (<-chan StreamEvent, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/responses", port)

	reqBody := responsesRequest{
		Model:  "openclaw:main",
		Input:  message,
		Stream: true,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.Debug("Streaming message to OpenClaw via SSE",
		"port", port,
		"message_preview", truncate(message, 50),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.streamHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}

	// Fallback: if gateway returns 404, the responses endpoint is not enabled (old container).
	// Use synchronous SendMessage and emit a single text_done event.
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		logger.Debug("Gateway /v1/responses returned 404, falling back to sync SendMessage")
		return c.fallbackSync(ctx, port, token, message)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("gateway returned HTTP %d", resp.StatusCode)
	}

	ch := make(chan StreamEvent, 64)
	go c.parseSSE(ctx, resp, ch)
	return ch, nil
}

// fallbackSync calls the synchronous SendMessage and wraps the result in a channel.
func (c *Client) fallbackSync(ctx context.Context, port int, token, message string) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 2)
	go func() {
		defer close(ch)
		text, err := c.SendMessage(ctx, port, token, message)
		if err != nil {
			ch <- StreamEvent{Type: "error", Text: err.Error()}
			return
		}
		ch <- StreamEvent{Type: "text_done", Text: text}
		ch <- StreamEvent{Type: "completed"}
	}()
	return ch, nil
}

// parseSSE reads SSE lines from the HTTP response and sends StreamEvents to the channel.
func (c *Client) parseSSE(ctx context.Context, resp *http.Response, ch chan<- StreamEvent) {
	defer close(ch)
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	// Allow large SSE lines (up to 1MB)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var eventType string
	var dataLines []string

	for scanner.Scan() {
		if ctx.Err() != nil {
			ch <- StreamEvent{Type: "error", Text: "context cancelled"}
			return
		}

		line := scanner.Text()

		if line == "" {
			// Empty line = end of event
			if len(dataLines) > 0 {
				data := strings.Join(dataLines, "\n")
				if evt, ok := c.mapSSEEvent(eventType, data); ok {
					ch <- evt
					if evt.Type == "completed" || evt.Type == "error" {
						return
					}
				}
			}
			eventType = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		} else if line == "data:" {
			dataLines = append(dataLines, "")
		}
	}

	// Process any remaining buffered event
	if len(dataLines) > 0 {
		data := strings.Join(dataLines, "\n")
		if evt, ok := c.mapSSEEvent(eventType, data); ok {
			ch <- evt
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		ch <- StreamEvent{Type: "error", Text: fmt.Sprintf("SSE read error: %v", err)}
	}
}

// mapSSEEvent maps an SSE event type + JSON data to a StreamEvent.
func (c *Client) mapSSEEvent(eventType, data string) (StreamEvent, bool) {
	switch eventType {
	case "response.output_text.delta":
		var payload struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err == nil {
			return StreamEvent{Type: "text_delta", Text: payload.Delta}, true
		}

	case "response.output_text.done":
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err == nil {
			return StreamEvent{Type: "text_done", Text: payload.Text}, true
		}

	case "response.completed":
		return StreamEvent{Type: "completed"}, true

	case "response.failed":
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &payload); err == nil {
			msg := payload.Error.Message
			if msg == "" {
				msg = "response failed"
			}
			return StreamEvent{Type: "error", Text: msg}, true
		}
		return StreamEvent{Type: "error", Text: "response failed"}, true
	}

	return StreamEvent{}, false
}
