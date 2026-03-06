package wechat

import (
	"encoding/xml"
	"fmt"
	"time"
)

// MsgType represents WeChat message types.
type MsgType string

const (
	MsgTypeText     MsgType = "text"
	MsgTypeImage    MsgType = "image"
	MsgTypeVoice    MsgType = "voice"
	MsgTypeVideo    MsgType = "video"
	MsgTypeLocation MsgType = "location"
	MsgTypeLink     MsgType = "link"
	MsgTypeEvent    MsgType = "event"
)

// EventType represents WeChat event types.
type EventType string

const (
	EventSubscribe   EventType = "subscribe"
	EventUnsubscribe EventType = "unsubscribe"
	EventScan        EventType = "SCAN"
	EventClick       EventType = "CLICK"
	EventView        EventType = "VIEW"
)

// IncomingMessage represents a message received from WeChat.
type IncomingMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"` // This is the user's OpenID
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      MsgType  `xml:"MsgType"`

	// Text message fields
	Content string `xml:"Content"`
	MsgId   int64  `xml:"MsgId"`

	// Image message fields
	PicUrl  string `xml:"PicUrl"`
	MediaId string `xml:"MediaId"`

	// Voice message fields
	Format      string `xml:"Format"`
	Recognition string `xml:"Recognition"` // Voice recognition result (if enabled)

	// Event fields
	Event    EventType `xml:"Event"`
	EventKey string    `xml:"EventKey"`
}

// ReplyMessage represents a reply message to WeChat.
type ReplyMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      MsgType  `xml:"MsgType"`
	Content      string   `xml:"Content,omitempty"`
}

// NewTextReply creates a text reply message.
func NewTextReply(toUser, fromUser, content string) *ReplyMessage {
	return &ReplyMessage{
		ToUserName:   toUser,
		FromUserName: fromUser,
		CreateTime:   time.Now().Unix(),
		MsgType:      MsgTypeText,
		Content:      content,
	}
}

// MarshalReply converts a ReplyMessage to XML bytes.
func MarshalReply(reply *ReplyMessage) ([]byte, error) {
	data, err := xml.Marshal(reply)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal reply: %w", err)
	}
	return data, nil
}

// ParseMessage parses an incoming WeChat XML message.
func ParseMessage(data []byte) (*IncomingMessage, error) {
	msg := &IncomingMessage{}
	if err := xml.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}
	return msg, nil
}
