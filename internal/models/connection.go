package models

import "time"

// Connection represents an active WebSocket connection
type Connection struct {
	ConnectionID string    `json:"connectionId" dynamodbav:"ConnectionId"`
	UserID       int       `json:"userId" dynamodbav:"UserId"`
	SystemID     string    `json:"systemId" dynamodbav:"SystemId"`
	ProgramID    int       `json:"programId" dynamodbav:"ProgramId"`
	Username     string    `json:"username,omitempty" dynamodbav:"Username,omitempty"`
	ConnectedAt  time.Time `json:"connectedAt" dynamodbav:"ConnectedAt"`
	LastPingAt   time.Time `json:"lastPingAt" dynamodbav:"LastPingAt"`
	TTL          int64     `json:"ttl" dynamodbav:"TTL"` // DynamoDB TTL for auto-cleanup
}

// Conversation represents a stored conversation between two users
type Conversation struct {
	ConversationID string    `json:"conversationId" dynamodbav:"ConversationId"` // PK: user1_user2 (sorted)
	MessageID      int       `json:"messageId" dynamodbav:"MessageId"`           // SK: auto-incrementing
	SenderID       int       `json:"senderId" dynamodbav:"SenderId"`
	RecipientID    int       `json:"recipientId" dynamodbav:"RecipientId"`
	Content        string    `json:"content" dynamodbav:"Content"`
	SentAt         time.Time `json:"sentAt" dynamodbav:"SentAt"`
	IsSeen         bool      `json:"isSeen" dynamodbav:"IsSeen"`
	ImageURL       string    `json:"imageUrl,omitempty" dynamodbav:"ImageUrl,omitempty"`
	TTL            int64     `json:"ttl" dynamodbav:"TTL"` // 30 days auto-cleanup
}

// ConversationRequest from client to fetch conversation history
type ConversationRequest struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	Limit                    int    `json:"Limit,omitempty"`
	LastMessageID            int    `json:"LastMessageId,omitempty"`
}

// ConversationResponse sent back to client with messages
type ConversationResponse struct {
	MessageType  string             `json:"MessageType"`
	Messages     []BroadcastMessage `json:"Messages"`
	HasMore      bool               `json:"HasMore"`
	TotalCount   int                `json:"TotalCount"`
}

// ActiveClientsResponse lists online users
type ActiveClientsResponse struct {
	MessageType   string       `json:"MessageType"`
	ActiveClients []UserStatus `json:"ActiveClients"`
}

// UserStatus represents a user's online status
type UserStatus struct {
	UserID      int       `json:"UserId"`
	Username    string    `json:"Username,omitempty"`
	IsOnline    bool      `json:"IsOnline"`
	LastSeenAt  time.Time `json:"LastSeenAt,omitempty"`
}

// NewsTracking tracks which news links have been sent to which users
type NewsTracking struct {
	LinkHash   string    `json:"linkHash" dynamodbav:"LinkHash"` // PK: SHA256 of URL
	UserID     int       `json:"userId" dynamodbav:"UserId"`     // SK
	URL        string    `json:"url" dynamodbav:"Url"`
	SentAt     time.Time `json:"sentAt" dynamodbav:"SentAt"`
	TTL        int64     `json:"ttl" dynamodbav:"TTL"` // 30 days
}

// PendingAnnouncement for offline users
type PendingAnnouncement struct {
	UserID           int                 `json:"userId" dynamodbav:"UserId"`           // PK
	AnnouncementID   string              `json:"announcementId" dynamodbav:"AnnouncementId"` // SK
	Announcement     AnnouncementMessage `json:"announcement" dynamodbav:"Announcement"`
	QueuedAt         time.Time           `json:"queuedAt" dynamodbav:"QueuedAt"`
	DeliveryAttempts int                 `json:"deliveryAttempts" dynamodbav:"DeliveryAttempts"`
	LastAttemptAt    *time.Time          `json:"lastAttemptAt,omitempty" dynamodbav:"LastAttemptAt,omitempty"`
	IsDelivered      bool                `json:"isDelivered" dynamodbav:"IsDelivered"`
	DeliveredAt      *time.Time          `json:"deliveredAt,omitempty" dynamodbav:"DeliveredAt,omitempty"`
	TTL              int64               `json:"ttl" dynamodbav:"TTL"`
}
