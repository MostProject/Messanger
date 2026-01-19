package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/MostProject/Messanger/internal/models"
	"github.com/MostProject/Messanger/internal/queue"
	"github.com/MostProject/Messanger/internal/services"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/google/uuid"
)

const AIClientID = 9999

// MessageHandler handles incoming WebSocket messages
type MessageHandler struct {
	store     *storage.DynamoDBStore
	s3Store   *storage.S3Store
	wsService *services.WebSocketService
	queue     *queue.SQSQueue
}

// NewMessageHandler creates a new message handler
func NewMessageHandler(
	store *storage.DynamoDBStore,
	s3Store *storage.S3Store,
	wsService *services.WebSocketService,
	queue *queue.SQSQueue,
) *MessageHandler {
	return &MessageHandler{
		store:     store,
		s3Store:   s3Store,
		wsService: wsService,
		queue:     queue,
	}
}

// HandleMessage processes an incoming WebSocket message
func (h *MessageHandler) HandleMessage(ctx context.Context, connectionID string, body string) error {
	// Parse base message to determine type
	var base models.BaseMessage
	if err := json.Unmarshal([]byte(body), &base); err != nil {
		return fmt.Errorf("invalid message format: %w", err)
	}

	switch base.MessageType {
	case models.TypeRegistration:
		return h.handleRegistration(ctx, connectionID, body)

	case models.TypeConversationRequest:
		return h.handleConversationRequest(ctx, connectionID, body)

	case models.TypeActiveClients:
		return h.handleActiveClientsRequest(ctx, connectionID)

	case models.TypePong:
		return h.handlePong(ctx, connectionID, body)

	case models.TypeImageUpload:
		return h.handleImageUpload(ctx, connectionID, body)

	case models.TypeImageDownload:
		return h.handleImageDownload(ctx, connectionID, body)

	case models.TypeReportRequest:
		return h.handleReportRequest(ctx, connectionID, body)

	case models.TypeReportModification:
		return h.handleReportModification(ctx, connectionID, body)

	case models.TypeSchemaResponse:
		return h.handleSchemaResponse(ctx, connectionID, body)

	case models.TypeErrorLog:
		return h.handleErrorLog(ctx, connectionID, body)

	default:
		// Regular chat message
		return h.handleChatMessage(ctx, connectionID, body)
	}
}

// handleRegistration processes client registration
func (h *MessageHandler) handleRegistration(ctx context.Context, connectionID string, body string) error {
	var reg models.RegistrationMessage
	if err := json.Unmarshal([]byte(body), &reg); err != nil {
		return h.wsService.SendError(ctx, connectionID, "registration_error", "Invalid registration format")
	}

	conn := &models.Connection{
		ConnectionID: connectionID,
		UserID:       reg.UserID,
		SystemID:     reg.SystemID,
		ProgramID:    reg.ProgramID,
		Username:     reg.Username,
		ConnectedAt:  time.Now().UTC(),
		LastPingAt:   time.Now().UTC(),
	}

	if err := h.store.SaveConnection(ctx, conn); err != nil {
		log.Printf("Failed to save connection: %v", err)
		return h.wsService.SendError(ctx, connectionID, "registration_error", "Failed to register")
	}

	// Send registration success
	response := map[string]interface{}{
		"MessageType": "registration_success",
		"UserId":      reg.UserID,
		"Message":     "Registration successful",
	}
	if err := h.wsService.SendToConnection(ctx, connectionID, response); err != nil {
		return err
	}

	// Deliver any pending announcements
	pendingAnnouncements, err := h.store.GetPendingAnnouncements(ctx, reg.UserID)
	if err != nil {
		log.Printf("Failed to get pending announcements: %v", err)
	} else {
		for _, pa := range pendingAnnouncements {
			if err := h.wsService.SendAnnouncement(ctx, &pa.Announcement, &reg.UserID); err == nil {
				h.store.MarkAnnouncementDelivered(ctx, reg.UserID, pa.AnnouncementID)
			}
		}
	}

	log.Printf("Client registered: UserID=%d, SystemID=%s, ConnectionID=%s", reg.UserID, reg.SystemID, connectionID)
	return nil
}

// handleConversationRequest retrieves conversation history
func (h *MessageHandler) handleConversationRequest(ctx context.Context, connectionID string, body string) error {
	var req models.ConversationRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return h.wsService.SendError(ctx, connectionID, "conversation_error", "Invalid request format")
	}

	messages, hasMore, err := h.store.GetConversation(
		ctx,
		req.MesajGonderenKullaniciID,
		req.MesajAliciKullaniciID,
		req.Limit,
		req.LastMessageID,
	)
	if err != nil {
		log.Printf("Failed to get conversation: %v", err)
		return h.wsService.SendError(ctx, connectionID, "conversation_error", "Failed to retrieve conversation")
	}

	// Mark messages as seen
	h.store.MarkMessagesSeen(ctx, req.MesajGonderenKullaniciID, req.MesajAliciKullaniciID)

	return h.wsService.SendConversation(ctx, connectionID, messages, hasMore)
}

// handleActiveClientsRequest returns list of online users
func (h *MessageHandler) handleActiveClientsRequest(ctx context.Context, connectionID string) error {
	return h.wsService.SendActiveClients(ctx, connectionID)
}

// handlePong processes pong response from client
func (h *MessageHandler) handlePong(ctx context.Context, connectionID string, body string) error {
	return h.store.UpdateConnectionPing(ctx, connectionID)
}

// handleImageUpload processes image uploads
func (h *MessageHandler) handleImageUpload(ctx context.Context, connectionID string, body string) error {
	var img models.ImageMessage
	if err := json.Unmarshal([]byte(body), &img); err != nil {
		return h.wsService.SendError(ctx, connectionID, "image_error", "Invalid image format")
	}

	// Upload to S3
	key, err := h.s3Store.UploadImage(ctx, img.ImageData, img.MesajGonderenKullaniciID, img.MesajAliciKullaniciID)
	if err != nil {
		log.Printf("Failed to upload image: %v", err)
		return h.wsService.SendError(ctx, connectionID, "image_error", err.Error())
	}

	// Save message reference to conversation
	msg := &models.IncomingMessage{
		MesajGonderenKullaniciID: img.MesajGonderenKullaniciID,
		MesajAliciKullaniciID:    img.MesajAliciKullaniciID,
		MesajIcerik:              fmt.Sprintf("[IMAGE:%s]", key),
		MesajGonderilenTarih:     img.MesajGonderilenTarih,
	}

	messageID, err := h.store.SaveMessage(ctx, msg)
	if err != nil {
		log.Printf("Failed to save image message: %v", err)
	}

	// Notify recipient
	h.wsService.SendMessage(ctx, msg, messageID)

	// Send confirmation to sender
	response := map[string]interface{}{
		"MessageType": "image_upload_success",
		"ImageKey":    key,
		"MessageId":   messageID,
	}
	return h.wsService.SendToConnection(ctx, connectionID, response)
}

// handleImageDownload provides presigned URL for image download
func (h *MessageHandler) handleImageDownload(ctx context.Context, connectionID string, body string) error {
	var req struct {
		MessageType string `json:"MessageType"`
		ImageKey    string `json:"ImageKey"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return h.wsService.SendError(ctx, connectionID, "image_error", "Invalid request format")
	}

	url, err := h.s3Store.GetImagePresignedURL(ctx, req.ImageKey)
	if err != nil {
		return h.wsService.SendError(ctx, connectionID, "image_error", "Failed to generate download URL")
	}

	response := map[string]interface{}{
		"MessageType": "image_download_response",
		"ImageKey":    req.ImageKey,
		"DownloadURL": url,
	}
	return h.wsService.SendToConnection(ctx, connectionID, response)
}

// handleReportRequest queues a report generation job
func (h *MessageHandler) handleReportRequest(ctx context.Context, connectionID string, body string) error {
	var req models.ReportRequestMessage
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return h.wsService.SendError(ctx, connectionID, "report_error", "Invalid request format")
	}

	// Get user info from connection
	conn, err := h.store.GetConnectionByUserID(ctx, req.MesajGonderenKullaniciID)
	if err != nil || conn == nil {
		return h.wsService.SendError(ctx, connectionID, "report_error", "User not registered")
	}

	// Create report job
	job := &models.ReportJob{
		JobID:        uuid.New().String(),
		UserID:       req.MesajGonderenKullaniciID,
		ConnectionID: connectionID,
		RequestID:    req.RequestID,
		Query:        "", // Will be filled from conversation context
		Status:       models.JobStatusPending,
		CreatedAt:    time.Now().UTC(),
	}

	// Save job to DynamoDB
	if err := h.store.SaveReportJob(ctx, job); err != nil {
		log.Printf("Failed to save report job: %v", err)
		return h.wsService.SendError(ctx, connectionID, "report_error", "Failed to queue report")
	}

	// Send to SQS queue
	if err := h.queue.EnqueueReportJob(ctx, job); err != nil {
		log.Printf("Failed to queue report job: %v", err)
		return h.wsService.SendError(ctx, connectionID, "report_error", "Failed to queue report")
	}

	// Send placeholder to user
	return h.wsService.SendReportPlaceholder(ctx, req.MesajGonderenKullaniciID, req.RequestID, "Report queued", 10, "Queued for processing")
}

// handleReportModification queues a report modification job
func (h *MessageHandler) handleReportModification(ctx context.Context, connectionID string, body string) error {
	var req models.ReportModificationMessage
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return h.wsService.SendError(ctx, connectionID, "report_error", "Invalid modification request")
	}

	job := &models.ReportJob{
		JobID:        uuid.New().String(),
		UserID:       req.MesajGonderenKullaniciID,
		ConnectionID: connectionID,
		RequestID:    req.OriginalReportID,
		Query:        req.ModificationQuery,
		Status:       models.JobStatusPending,
		CreatedAt:    time.Now().UTC(),
	}

	if err := h.store.SaveReportJob(ctx, job); err != nil {
		return h.wsService.SendError(ctx, connectionID, "report_error", "Failed to save modification job")
	}

	if err := h.queue.EnqueueReportJob(ctx, job); err != nil {
		return h.wsService.SendError(ctx, connectionID, "report_error", "Failed to queue modification")
	}

	return h.wsService.SendReportPlaceholder(ctx, req.MesajGonderenKullaniciID, req.OriginalReportID, "Modification queued", 10, "Processing modifications")
}

// handleSchemaResponse processes schema response from client
func (h *MessageHandler) handleSchemaResponse(ctx context.Context, connectionID string, body string) error {
	var resp models.SchemaResponseMessage
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return h.wsService.SendError(ctx, connectionID, "schema_error", "Invalid schema response")
	}

	// Update the corresponding job with schema info
	job, err := h.store.GetReportJob(ctx, resp.RequestID)
	if err != nil || job == nil {
		return h.wsService.SendError(ctx, connectionID, "schema_error", "Report job not found")
	}

	job.Schema = resp.TablesAndColumns

	if err := h.store.SaveReportJob(ctx, job); err != nil {
		return h.wsService.SendError(ctx, connectionID, "schema_error", "Failed to update job with schema")
	}

	// Re-queue the job with schema
	return h.queue.EnqueueReportJob(ctx, job)
}

// handleErrorLog stores client error logs
func (h *MessageHandler) handleErrorLog(ctx context.Context, connectionID string, body string) error {
	var errLog models.ErrorLogMessage
	if err := json.Unmarshal([]byte(body), &errLog); err != nil {
		return nil // Silently ignore invalid error logs
	}

	log.Printf("Client error [%s]: %s - %s", errLog.ErrorType, errLog.ErrorMessage, errLog.StackTrace)
	return nil
}

// handleChatMessage processes regular chat messages
func (h *MessageHandler) handleChatMessage(ctx context.Context, connectionID string, body string) error {
	var msg models.IncomingMessage
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		return h.wsService.SendError(ctx, connectionID, "message_error", "Invalid message format")
	}

	if msg.MesajIcerik == "" {
		return h.wsService.SendError(ctx, connectionID, "message_error", "Message content is empty")
	}

	// Save message
	messageID, err := h.store.SaveMessage(ctx, &msg)
	if err != nil {
		log.Printf("Failed to save message: %v", err)
		return h.wsService.SendError(ctx, connectionID, "message_error", "Failed to save message")
	}

	// Route message to recipient
	if err := h.wsService.SendMessage(ctx, &msg, messageID); err != nil {
		log.Printf("Failed to route message: %v", err)
	}

	// Check if this is a message to AI
	if msg.MesajAliciKullaniciID == AIClientID {
		// Queue for AI processing
		job := &models.ReportJob{
			JobID:        uuid.New().String(),
			UserID:       msg.MesajGonderenKullaniciID,
			ConnectionID: connectionID,
			RequestID:    fmt.Sprintf("ai_%d", messageID),
			Query:        msg.MesajIcerik,
			Status:       models.JobStatusPending,
			CreatedAt:    time.Now().UTC(),
		}
		h.store.SaveReportJob(ctx, job)
		h.queue.EnqueueReportJob(ctx, job)
	}

	// Send confirmation to sender
	response := map[string]interface{}{
		"MessageType": "message_sent",
		"MessageId":   messageID,
	}
	return h.wsService.SendToConnection(ctx, connectionID, response)
}
