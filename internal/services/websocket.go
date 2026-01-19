package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/MostProject/Messanger/internal/models"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
)

// WebSocketService handles WebSocket communication via API Gateway
type WebSocketService struct {
	apiClient *apigatewaymanagementapi.Client
	store     *storage.DynamoDBStore
}

// NewWebSocketService creates a new WebSocket service
func NewWebSocketService(apiClient *apigatewaymanagementapi.Client, store *storage.DynamoDBStore) *WebSocketService {
	return &WebSocketService{
		apiClient: apiClient,
		store:     store,
	}
}

// SendToConnection sends data to a specific WebSocket connection
func (s *WebSocketService) SendToConnection(ctx context.Context, connectionID string, data interface{}) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	_, err = s.apiClient.PostToConnection(ctx, &apigatewaymanagementapi.PostToConnectionInput{
		ConnectionId: aws.String(connectionID),
		Data:         jsonData,
	})
	if err != nil {
		// Check if connection is stale and clean up
		log.Printf("Failed to send to connection %s: %v", connectionID, err)
		// Don't return error for stale connections, just log it
		return nil
	}

	return nil
}

// SendToUser sends data to a user by looking up their connection
func (s *WebSocketService) SendToUser(ctx context.Context, userID int, data interface{}) error {
	conn, err := s.store.GetConnectionByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to get connection for user %d: %w", userID, err)
	}

	if conn == nil {
		// User is offline - this is not an error
		return nil
	}

	return s.SendToConnection(ctx, conn.ConnectionID, data)
}

// BroadcastToAll sends data to all connected clients
func (s *WebSocketService) BroadcastToAll(ctx context.Context, data interface{}) error {
	connections, err := s.store.GetAllConnections(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connections: %w", err)
	}

	for _, conn := range connections {
		if err := s.SendToConnection(ctx, conn.ConnectionID, data); err != nil {
			log.Printf("Failed to broadcast to connection %s: %v", conn.ConnectionID, err)
			// Continue broadcasting to other connections
		}
	}

	return nil
}

// SendMessage routes a message to the recipient
func (s *WebSocketService) SendMessage(ctx context.Context, msg *models.IncomingMessage, messageID int) error {
	// Create broadcast message
	broadcast := models.BroadcastMessage{
		MesajID:                  messageID,
		MesajGonderenKullaniciID: msg.MesajGonderenKullaniciID,
		MesajAliciKullaniciID:    msg.MesajAliciKullaniciID,
		MesajIcerik:              msg.MesajIcerik,
		MesajGonderilenTarih:     msg.MesajGonderilenTarih,
		IsSeenByRecipient:        false,
	}

	// Send to recipient
	return s.SendToUser(ctx, msg.MesajAliciKullaniciID, broadcast)
}

// SendAnnouncement sends an announcement to a specific user or broadcasts
func (s *WebSocketService) SendAnnouncement(ctx context.Context, announcement *models.AnnouncementMessage, userID *int) error {
	announcement.MessageType = models.TypeAnnouncement

	if userID != nil {
		return s.SendToUser(ctx, *userID, announcement)
	}
	return s.BroadcastToAll(ctx, announcement)
}

// SendReportPlaceholder sends a placeholder while report is being generated
func (s *WebSocketService) SendReportPlaceholder(ctx context.Context, userID int, requestID string, status string, progress int, step string) error {
	placeholder := models.ReportPlaceholderMessage{
		MessageType:              models.TypeReportPlaceholder,
		MesajGonderenKullaniciID: 9999, // AI user
		MesajAliciKullaniciID:    userID,
		RequestID:                requestID,
		StatusMessage:            status,
		ProgressPercentage:       progress,
		CurrentStep:              step,
	}

	return s.SendToUser(ctx, userID, placeholder)
}

// SendReport sends a generated report to the user
func (s *WebSocketService) SendReport(ctx context.Context, userID int, report *models.ReportMessage) error {
	report.MessageType = models.TypeReport
	return s.SendToUser(ctx, userID, report)
}

// SendActiveClients sends the list of active clients to a user
func (s *WebSocketService) SendActiveClients(ctx context.Context, connectionID string) error {
	connections, err := s.store.GetAllConnections(ctx)
	if err != nil {
		return fmt.Errorf("failed to get connections: %w", err)
	}

	users := make([]models.UserStatus, 0, len(connections))
	for _, conn := range connections {
		users = append(users, models.UserStatus{
			UserID:     conn.UserID,
			Username:   conn.Username,
			IsOnline:   true,
			LastSeenAt: conn.LastPingAt,
		})
	}

	response := models.ActiveClientsResponse{
		MessageType:   "active_clients_response",
		ActiveClients: users,
	}

	return s.SendToConnection(ctx, connectionID, response)
}

// SendConversation sends conversation history to a user
func (s *WebSocketService) SendConversation(ctx context.Context, connectionID string, messages []models.BroadcastMessage, hasMore bool) error {
	response := models.ConversationResponse{
		MessageType: "conversation_response",
		Messages:    messages,
		HasMore:     hasMore,
		TotalCount:  len(messages),
	}

	return s.SendToConnection(ctx, connectionID, response)
}

// SendPing sends a ping message to a connection
func (s *WebSocketService) SendPing(ctx context.Context, connectionID string, pingID string, timestamp int64) error {
	ping := models.PingMessage{
		MessageType: models.TypePing,
		Timestamp:   timestamp,
		PingID:      pingID,
	}

	return s.SendToConnection(ctx, connectionID, ping)
}

// SendError sends an error message to a connection
func (s *WebSocketService) SendError(ctx context.Context, connectionID string, errorType string, errorMessage string) error {
	errMsg := map[string]string{
		"MessageType":  "error",
		"ErrorType":    errorType,
		"ErrorMessage": errorMessage,
	}

	return s.SendToConnection(ctx, connectionID, errMsg)
}
