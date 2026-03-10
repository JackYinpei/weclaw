package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/qcy/weclaw/pkg/logger"
)

// Client communicates with an OpenClaw Gateway instance via HTTP API.
type Client struct {
	httpClient       *http.Client
	streamHTTPClient *http.Client // No timeout — SSE connections rely on ctx cancellation
}

// NewClient creates a new OpenClaw API client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		streamHTTPClient: &http.Client{}, // No timeout for SSE streaming
	}
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// SendMessage sends a text message to an OpenClaw Gateway HTTP endpoint and returns the response.
func (c *Client) SendMessage(ctx context.Context, port int, token, message string) (string, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port)

	reqBody := chatCompletionRequest{
		Model: "openclaw:main",
		Messages: []chatMessage{
			{Role: "user", Content: message},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.Debug("Sending message to OpenClaw via HTTP",
		"port", port,
		"message_preview", truncate(message, 50),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gateway returned HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var result chatCompletionResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w, raw: %s", err, truncate(string(respBody), 200))
	}

	if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		return "[无响应内容]", nil
	}

	text := result.Choices[0].Message.Content

	logger.Debug("Received response from OpenClaw",
		"port", port,
		"response_preview", truncate(text, 100),
	)

	return text, nil
}

// CheckHealth checks if the OpenClaw Gateway HTTP endpoint is accepting connections.
func (c *Client) CheckHealth(port int, token string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	url := fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodOptions, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	// Any non-connection-error response means the gateway is up
	return true
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
