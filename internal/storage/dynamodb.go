package storage

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/MostProject/Messanger/internal/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	ConnectionsTable     = "messanger-connections"
	ConversationsTable   = "messanger-conversations"
	AnnouncementsTable   = "messanger-announcements"
	ReportJobsTable      = "messanger-report-jobs"
	NewsTrackingTable    = "messanger-news-tracking"

	DefaultTTLDays       = 30
	ConnectionTTLMinutes = 60
)

// DynamoDBStore handles all DynamoDB operations
type DynamoDBStore struct {
	client *dynamodb.Client
}

// NewDynamoDBStore creates a new DynamoDB store
func NewDynamoDBStore(client *dynamodb.Client) *DynamoDBStore {
	return &DynamoDBStore{client: client}
}

// --- Connection Management ---

// SaveConnection saves a new WebSocket connection
func (s *DynamoDBStore) SaveConnection(ctx context.Context, conn *models.Connection) error {
	conn.TTL = time.Now().Add(ConnectionTTLMinutes * time.Minute).Unix()

	item, err := attributevalue.MarshalMap(conn)
	if err != nil {
		return fmt.Errorf("failed to marshal connection: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ConnectionsTable),
		Item:      item,
	})
	return err
}

// DeleteConnection removes a WebSocket connection
func (s *DynamoDBStore) DeleteConnection(ctx context.Context, connectionID string) error {
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(ConnectionsTable),
		Key: map[string]types.AttributeValue{
			"ConnectionId": &types.AttributeValueMemberS{Value: connectionID},
		},
	})
	return err
}

// GetConnectionByUserID finds a connection by user ID
func (s *DynamoDBStore) GetConnectionByUserID(ctx context.Context, userID int) (*models.Connection, error) {
	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ConnectionsTable),
		IndexName:              aws.String("UserId-index"),
		KeyConditionExpression: aws.String("UserId = :uid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":uid": &types.AttributeValueMemberN{Value: strconv.Itoa(userID)},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, err
	}

	if len(result.Items) == 0 {
		return nil, nil
	}

	var conn models.Connection
	if err := attributevalue.UnmarshalMap(result.Items[0], &conn); err != nil {
		return nil, err
	}
	return &conn, nil
}

// GetAllConnections returns all active connections
func (s *DynamoDBStore) GetAllConnections(ctx context.Context) ([]models.Connection, error) {
	result, err := s.client.Scan(ctx, &dynamodb.ScanInput{
		TableName: aws.String(ConnectionsTable),
	})
	if err != nil {
		return nil, err
	}

	var connections []models.Connection
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &connections); err != nil {
		return nil, err
	}
	return connections, nil
}

// UpdateConnectionPing updates the last ping time for a connection
func (s *DynamoDBStore) UpdateConnectionPing(ctx context.Context, connectionID string) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ConnectionsTable),
		Key: map[string]types.AttributeValue{
			"ConnectionId": &types.AttributeValueMemberS{Value: connectionID},
		},
		UpdateExpression: aws.String("SET LastPingAt = :ping, #ttl = :ttl"),
		ExpressionAttributeNames: map[string]string{
			"#ttl": "TTL",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":ping": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
			":ttl":  &types.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Add(ConnectionTTLMinutes*time.Minute).Unix(), 10)},
		},
	})
	return err
}

// --- Message/Conversation Management ---

// getConversationID creates a consistent conversation ID from two user IDs
func getConversationID(user1, user2 int) string {
	ids := []int{user1, user2}
	sort.Ints(ids)
	return fmt.Sprintf("%d_%d", ids[0], ids[1])
}

// SaveMessage saves a message to a conversation
func (s *DynamoDBStore) SaveMessage(ctx context.Context, msg *models.IncomingMessage) (int, error) {
	convID := getConversationID(msg.MesajGonderenKullaniciID, msg.MesajAliciKullaniciID)

	// Get next message ID using atomic counter
	counterResult, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ConversationsTable),
		Key: map[string]types.AttributeValue{
			"ConversationId": &types.AttributeValueMemberS{Value: convID},
			"MessageId":      &types.AttributeValueMemberN{Value: "0"}, // Counter record
		},
		UpdateExpression: aws.String("ADD MessageCounter :inc"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":inc": &types.AttributeValueMemberN{Value: "1"},
		},
		ReturnValues: types.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get message counter: %w", err)
	}

	messageID := 1
	if counter, ok := counterResult.Attributes["MessageCounter"]; ok {
		if n, ok := counter.(*types.AttributeValueMemberN); ok {
			messageID, _ = strconv.Atoi(n.Value)
		}
	}

	sentAt, _ := time.Parse("2006-01-02 15:04:05", msg.MesajGonderilenTarih)
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}

	conv := models.Conversation{
		ConversationID: convID,
		MessageID:      messageID,
		SenderID:       msg.MesajGonderenKullaniciID,
		RecipientID:    msg.MesajAliciKullaniciID,
		Content:        msg.MesajIcerik,
		SentAt:         sentAt,
		IsSeen:         false,
		TTL:            time.Now().AddDate(0, 0, DefaultTTLDays).Unix(),
	}

	item, err := attributevalue.MarshalMap(conv)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal conversation: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ConversationsTable),
		Item:      item,
	})
	if err != nil {
		return 0, err
	}

	return messageID, nil
}

// GetConversation retrieves messages between two users
func (s *DynamoDBStore) GetConversation(ctx context.Context, user1, user2, limit int, lastMsgID int) ([]models.BroadcastMessage, bool, error) {
	convID := getConversationID(user1, user2)

	if limit <= 0 {
		limit = 50
	}

	input := &dynamodb.QueryInput{
		TableName:              aws.String(ConversationsTable),
		KeyConditionExpression: aws.String("ConversationId = :cid AND MessageId > :mid"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid": &types.AttributeValueMemberS{Value: convID},
			":mid": &types.AttributeValueMemberN{Value: strconv.Itoa(lastMsgID)},
		},
		Limit:            aws.Int32(int32(limit + 1)), // +1 to check if there's more
		ScanIndexForward: aws.Bool(false),             // Newest first
	}

	result, err := s.client.Query(ctx, input)
	if err != nil {
		return nil, false, err
	}

	var conversations []models.Conversation
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &conversations); err != nil {
		return nil, false, err
	}

	hasMore := len(conversations) > limit
	if hasMore {
		conversations = conversations[:limit]
	}

	// Convert to broadcast messages
	messages := make([]models.BroadcastMessage, len(conversations))
	for i, conv := range conversations {
		messages[i] = models.BroadcastMessage{
			MesajID:                  conv.MessageID,
			MesajGonderenKullaniciID: conv.SenderID,
			MesajAliciKullaniciID:    conv.RecipientID,
			MesajIcerik:              conv.Content,
			MesajGonderilenTarih:     conv.SentAt.Format("2006-01-02 15:04:05"),
			IsSeenByRecipient:        conv.IsSeen,
		}
	}

	return messages, hasMore, nil
}

// MarkMessagesSeen marks messages as seen by recipient
func (s *DynamoDBStore) MarkMessagesSeen(ctx context.Context, user1, user2 int) error {
	convID := getConversationID(user1, user2)

	// Query unseen messages
	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ConversationsTable),
		KeyConditionExpression: aws.String("ConversationId = :cid"),
		FilterExpression:       aws.String("RecipientId = :rid AND IsSeen = :seen"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":cid":  &types.AttributeValueMemberS{Value: convID},
			":rid":  &types.AttributeValueMemberN{Value: strconv.Itoa(user1)},
			":seen": &types.AttributeValueMemberBOOL{Value: false},
		},
	})
	if err != nil {
		return err
	}

	// Update each message
	for _, item := range result.Items {
		msgID := item["MessageId"]
		_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(ConversationsTable),
			Key: map[string]types.AttributeValue{
				"ConversationId": &types.AttributeValueMemberS{Value: convID},
				"MessageId":      msgID,
			},
			UpdateExpression: aws.String("SET IsSeen = :seen"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":seen": &types.AttributeValueMemberBOOL{Value: true},
			},
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// --- Report Job Management ---

// SaveReportJob saves a new report job
func (s *DynamoDBStore) SaveReportJob(ctx context.Context, job *models.ReportJob) error {
	item, err := attributevalue.MarshalMap(job)
	if err != nil {
		return fmt.Errorf("failed to marshal report job: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ReportJobsTable),
		Item:      item,
	})
	return err
}

// GetReportJob retrieves a report job by ID
func (s *DynamoDBStore) GetReportJob(ctx context.Context, jobID string) (*models.ReportJob, error) {
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ReportJobsTable),
		Key: map[string]types.AttributeValue{
			"JobId": &types.AttributeValueMemberS{Value: jobID},
		},
	})
	if err != nil {
		return nil, err
	}

	if result.Item == nil {
		return nil, nil
	}

	var job models.ReportJob
	if err := attributevalue.UnmarshalMap(result.Item, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// UpdateReportJobStatus updates the status of a report job
func (s *DynamoDBStore) UpdateReportJobStatus(ctx context.Context, jobID string, status models.JobStatus, result string, errMsg string) error {
	now := time.Now().UTC()
	updateExpr := "SET #status = :status, CompletedAt = :completed"
	exprValues := map[string]types.AttributeValue{
		":status":    &types.AttributeValueMemberS{Value: string(status)},
		":completed": &types.AttributeValueMemberS{Value: now.Format(time.RFC3339)},
	}

	if result != "" {
		updateExpr += ", #result = :result"
		exprValues[":result"] = &types.AttributeValueMemberS{Value: result}
	}
	if errMsg != "" {
		updateExpr += ", #error = :error"
		exprValues[":error"] = &types.AttributeValueMemberS{Value: errMsg}
	}

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(ReportJobsTable),
		Key: map[string]types.AttributeValue{
			"JobId": &types.AttributeValueMemberS{Value: jobID},
		},
		UpdateExpression: aws.String(updateExpr),
		ExpressionAttributeNames: map[string]string{
			"#status": "Status",
			"#result": "Result",
			"#error":  "Error",
		},
		ExpressionAttributeValues: exprValues,
	})
	return err
}

// --- News Tracking ---

// HasLinkBeenSentToUser checks if a news link was already sent to a user
func (s *DynamoDBStore) HasLinkBeenSentToUser(ctx context.Context, url string, userID int) (bool, error) {
	linkHash := fmt.Sprintf("%x", sha256.Sum256([]byte(url)))

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(NewsTrackingTable),
		Key: map[string]types.AttributeValue{
			"LinkHash": &types.AttributeValueMemberS{Value: linkHash},
			"UserId":   &types.AttributeValueMemberN{Value: strconv.Itoa(userID)},
		},
	})
	if err != nil {
		return false, err
	}

	return result.Item != nil, nil
}

// MarkLinkSentToUser marks a news link as sent to a user
func (s *DynamoDBStore) MarkLinkSentToUser(ctx context.Context, url string, userID int) error {
	linkHash := fmt.Sprintf("%x", sha256.Sum256([]byte(url)))

	tracking := models.NewsTracking{
		LinkHash: linkHash,
		UserID:   userID,
		URL:      url,
		SentAt:   time.Now().UTC(),
		TTL:      time.Now().AddDate(0, 0, DefaultTTLDays).Unix(),
	}

	item, err := attributevalue.MarshalMap(tracking)
	if err != nil {
		return err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(NewsTrackingTable),
		Item:      item,
	})
	return err
}

// --- Pending Announcements ---

// SavePendingAnnouncement saves an announcement for an offline user
func (s *DynamoDBStore) SavePendingAnnouncement(ctx context.Context, pa *models.PendingAnnouncement) error {
	pa.TTL = time.Now().AddDate(0, 0, 7).Unix() // 7 days TTL for pending announcements

	item, err := attributevalue.MarshalMap(pa)
	if err != nil {
		return err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(AnnouncementsTable),
		Item:      item,
	})
	return err
}

// GetPendingAnnouncements gets all pending announcements for a user
func (s *DynamoDBStore) GetPendingAnnouncements(ctx context.Context, userID int) ([]models.PendingAnnouncement, error) {
	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(AnnouncementsTable),
		KeyConditionExpression: aws.String("UserId = :uid"),
		FilterExpression:       aws.String("IsDelivered = :delivered"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":uid":       &types.AttributeValueMemberN{Value: strconv.Itoa(userID)},
			":delivered": &types.AttributeValueMemberBOOL{Value: false},
		},
	})
	if err != nil {
		return nil, err
	}

	var announcements []models.PendingAnnouncement
	if err := attributevalue.UnmarshalListOfMaps(result.Items, &announcements); err != nil {
		return nil, err
	}
	return announcements, nil
}

// MarkAnnouncementDelivered marks an announcement as delivered
func (s *DynamoDBStore) MarkAnnouncementDelivered(ctx context.Context, userID int, announcementID string) error {
	now := time.Now().UTC()
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(AnnouncementsTable),
		Key: map[string]types.AttributeValue{
			"UserId":         &types.AttributeValueMemberN{Value: strconv.Itoa(userID)},
			"AnnouncementId": &types.AttributeValueMemberS{Value: announcementID},
		},
		UpdateExpression: aws.String("SET IsDelivered = :delivered, DeliveredAt = :at"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":delivered": &types.AttributeValueMemberBOOL{Value: true},
			":at":        &types.AttributeValueMemberS{Value: now.Format(time.RFC3339)},
		},
	})
	return err
}
