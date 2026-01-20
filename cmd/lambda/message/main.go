package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/MostProject/Messanger/internal/handlers"
	"github.com/MostProject/Messanger/internal/observability"
	"github.com/MostProject/Messanger/internal/queue"
	"github.com/MostProject/Messanger/internal/services"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Reusable clients initialized once at cold start
var (
	awsCfg   aws.Config
	ddbStore *storage.DynamoDBStore
	s3Store  *storage.S3Store
	sqsQueue *queue.SQSQueue
	logger   *observability.Logger
	metrics  *observability.Metrics
)

func init() {
	var err error
	awsCfg, err = config.LoadDefaultConfig(context.Background())
	if err != nil {
		panic(fmt.Sprintf("Failed to load AWS config: %v", err))
	}

	// Initialize logger
	logger = observability.NewLogger("lambda-message", observability.LevelInfo)

	// Initialize clients once - reused across invocations
	ddbClient := dynamodb.NewFromConfig(awsCfg)
	s3Client := s3.NewFromConfig(awsCfg)
	sqsClient := sqs.NewFromConfig(awsCfg)
	cwClient := cloudwatch.NewFromConfig(awsCfg)

	// Initialize stores
	ddbStore = storage.NewDynamoDBStore(ddbClient)
	s3Store = storage.NewS3Store(s3Client)

	// Validate queue URL
	queueURL := os.Getenv("REPORT_QUEUE_URL")
	if queueURL == "" {
		logger.Warn(context.Background(), "REPORT_QUEUE_URL not set, report jobs will fail", nil)
	}
	sqsQueue = queue.NewSQSQueue(sqsClient, queueURL)

	// Initialize metrics
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "dev"
	}
	metrics = observability.NewMetrics(cwClient, "Messanger/Lambda", env)

	logger.Info(context.Background(), "Lambda initialized successfully", map[string]interface{}{
		"queue_configured": queueURL != "",
	})
}

func handler(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	start := time.Now()
	connectionID := event.RequestContext.ConnectionID
	body := event.Body
	requestID := event.RequestContext.RequestID

	// Add context values for logging
	ctx = observability.WithConnectionID(ctx, connectionID)
	ctx = observability.WithRequestID(ctx, requestID)

	// Validate input
	if connectionID == "" {
		logger.Error(ctx, "Empty connection ID", nil, nil)
		metrics.Counter(observability.MetricErrorsTotal, 1)
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       "Invalid connection",
		}, nil
	}

	if body == "" {
		logger.Error(ctx, "Empty message body", nil, nil)
		metrics.Counter(observability.MetricErrorsTotal, 1)
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       "Empty message",
		}, nil
	}

	// Log request (truncate body for security)
	logBody := body
	if len(logBody) > 200 {
		logBody = logBody[:200] + "..."
	}
	logger.Debug(ctx, "Processing message", map[string]interface{}{
		"body_preview":  logBody,
		"body_length":   len(body),
		"connection_id": connectionID,
	})

	// Create API Gateway Management client per-request (endpoint varies)
	endpoint := fmt.Sprintf("https://%s/%s", event.RequestContext.DomainName, event.RequestContext.Stage)
	apiClient := apigatewaymanagementapi.NewFromConfig(awsCfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = &endpoint
	})

	// Create WebSocket service with per-request API client but reuse stores
	wsService := services.NewWebSocketService(apiClient, ddbStore)

	// Create handler with reused stores and per-request WebSocket service
	msgHandler := handlers.NewMessageHandler(ddbStore, s3Store, wsService, sqsQueue)

	// Handle the message
	if err := msgHandler.HandleMessage(ctx, connectionID, body); err != nil {
		logger.Error(ctx, "Error handling message", err, map[string]interface{}{
			"connection_id": connectionID,
			"duration_ms":   time.Since(start).Milliseconds(),
		})
		metrics.Counter(observability.MetricMessagesFailed, 1)
		metrics.RecordAPICall(ctx, "HandleMessage", time.Since(start), err)
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Body:       "Error processing message",
		}, nil
	}

	// Record success metrics
	metrics.Counter(observability.MetricMessagesProcessed, 1)
	metrics.RecordAPICall(ctx, "HandleMessage", time.Since(start), nil)

	logger.Info(ctx, "Message processed successfully", map[string]interface{}{
		"duration_ms": time.Since(start).Milliseconds(),
	})

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Body:       "OK",
	}, nil
}

func main() {
	lambda.Start(handler)
}
