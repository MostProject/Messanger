package models

import "time"

// MessageType constants
const (
	TypeRegistration        = "registration"
	TypeConversationRequest = "conversation_request"
	TypeActiveClients       = "active_clients_request"
	TypeErrorLog            = "error_log"
	TypePing                = "ping"
	TypePong                = "pong"
	TypeImageUpload         = "image_upload"
	TypeImageDownload       = "image_download_request"
	TypeSchemaRequest       = "schema_request"
	TypeSchemaResponse      = "schema_response"
	TypeReportRequest       = "report_request"
	TypeReportModification  = "report_modification"
	TypeReportPlaceholder   = "report_placeholder"
	TypeReport              = "report"
	TypeAnnouncement        = "announcement"
	TypeServerMessage       = "server_message"
)

// BaseMessage contains common fields for all messages
type BaseMessage struct {
	MessageType string `json:"MessageType" dynamodbav:"MessageType"`
}

// IncomingMessage represents a message sent between users
type IncomingMessage struct {
	MessageType            string `json:"MessageType,omitempty" dynamodbav:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId" dynamodbav:"SenderId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId" dynamodbav:"RecipientId"`
	MesajIcerik              string `json:"Mesaj_Icerik" dynamodbav:"Content"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih" dynamodbav:"SentAt"`
}

// BroadcastMessage represents a message to be broadcast to recipient
type BroadcastMessage struct {
	MesajID                  int    `json:"Mesaj_Id" dynamodbav:"MessageId"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId" dynamodbav:"SenderId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId" dynamodbav:"RecipientId"`
	MesajIcerik              string `json:"Mesaj_Icerik" dynamodbav:"Content"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih" dynamodbav:"SentAt"`
	IsSeenByRecipient        bool   `json:"IsSeenByRecipient" dynamodbav:"IsSeen"`
}

// RegistrationMessage for client registration
type RegistrationMessage struct {
	MessageType string `json:"MessageType"`
	SystemID    string `json:"SystemId"`
	ProgramID   int    `json:"ProgramId"`
	UserID      int    `json:"UserId"`
	Username    string `json:"Username,omitempty"`
}

// PingMessage for connection keepalive
type PingMessage struct {
	MessageType string `json:"MessageType"`
	Timestamp   int64  `json:"Timestamp"`
	PingID      string `json:"PingId"`
}

// PongMessage response to ping
type PongMessage struct {
	MessageType string `json:"MessageType"`
	Timestamp   int64  `json:"Timestamp"`
	PingID      string `json:"PingId"`
}

// ImageMessage for image uploads
type ImageMessage struct {
	MessageType              string `json:"MessageType"`
	MesajGonderenKullaniciID int    `json:"Mesaj_GonderenKullaniciId"`
	MesajAliciKullaniciID    int    `json:"Mesaj_AliciKullaniciId"`
	MesajGonderilenTarih     string `json:"Mesaj_GonderilenTarih"`
	ImageData                string `json:"ImageData"`
	ImageName                string `json:"ImageName"`
	ImageSize                int64  `json:"ImageSize"`
}

// AnnouncementType enum
type AnnouncementType int

const (
	AnnouncementTypeNews AnnouncementType = iota
	AnnouncementTypeSystem
)

// AnnouncementMessage for system and news announcements
type AnnouncementMessage struct {
	MessageType    string           `json:"MessageType" dynamodbav:"MessageType"`
	AnnouncementID string           `json:"AnnouncementId" dynamodbav:"AnnouncementId"`
	Type           AnnouncementType `json:"Type" dynamodbav:"Type"`
	Title          string           `json:"Title" dynamodbav:"Title"`
	Description    string           `json:"Description" dynamodbav:"Description"`
	Link           string           `json:"Link,omitempty" dynamodbav:"Link,omitempty"`
	Source         string           `json:"Source,omitempty" dynamodbav:"Source,omitempty"`
	CreatedDate    time.Time        `json:"CreatedDate" dynamodbav:"CreatedDate"`
	ExpirationDate *time.Time       `json:"ExpirationDate,omitempty" dynamodbav:"ExpirationDate,omitempty"`
	Priority       int              `json:"Priority" dynamodbav:"Priority"`
}

// ServerMessageType enum
type ServerMessageType int

const (
	ServerMessageTypeDirect ServerMessageType = iota
	ServerMessageTypeBroadcast
	ServerMessageTypeWarning
	ServerMessageTypeInfo
)

// ServerMessage for server-originated messages
type ServerMessage struct {
	MessageType       string            `json:"MessageType"`
	MessageID         string            `json:"MessageId"`
	MesajAliciKullaniciID int           `json:"Mesaj_AliciKullaniciId"`
	MesajIcerik       string            `json:"Mesaj_Icerik"`
	MesajGonderilenTarih string         `json:"Mesaj_GonderilenTarih"`
	ServerMessageType ServerMessageType `json:"ServerMessageType"`
	Priority          int               `json:"Priority"`
}

// ErrorLogMessage for client error reporting
type ErrorLogMessage struct {
	MessageType    string    `json:"MessageType"`
	Timestamp      time.Time `json:"Timestamp"`
	ErrorType      string    `json:"ErrorType"`
	ErrorMessage   string    `json:"ErrorMessage"`
	StackTrace     string    `json:"StackTrace"`
	AdditionalInfo string    `json:"AdditionalInfo"`
}
