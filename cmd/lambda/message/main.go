package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/MostProject/Messanger/internal/handlers"
	"github.com/MostProject/Messanger/internal/queue"
	"github.com/MostProject/Messanger/internal/services"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
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
)

func init() {
	var err error
	awsCfg, err = config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Initialize clients once - reused across invocations
	ddbClient := dynamodb.NewFromConfig(awsCfg)
	s3Client := s3.NewFromConfig(awsCfg)
	sqsClient := sqs.NewFromConfig(awsCfg)

	// Initialize stores
	ddbStore = storage.NewDynamoDBStore(ddbClient)
	s3Store = storage.NewS3Store(s3Client)

	// Validate queue URL
	queueURL := os.Getenv("REPORT_QUEUE_URL")
	if queueURL == "" {
		log.Println("WARNING: REPORT_QUEUE_URL not set, report jobs will fail")
	}
	sqsQueue = queue.NewSQSQueue(sqsClient, queueURL)

	log.Println("Lambda initialized successfully")
}

func handler(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	connectionID := event.RequestContext.ConnectionID
	body := event.Body

	// Validate input
	if connectionID == "" {
		log.Println("Error: empty connection ID")
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       "Invalid connection",
		}, nil
	}

	if body == "" {
		log.Println("Error: empty message body")
		return events.APIGatewayProxyResponse{
			StatusCode: 400,
			Body:       "Empty message",
		}, nil
	}

	// Truncate body for logging (avoid logging sensitive data)
	logBody := body
	if len(logBody) > 200 {
		logBody = logBody[:200] + "..."
	}
	log.Printf("Message from %s: %s", connectionID, logBody)

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
		log.Printf("Error handling message from %s: %v", connectionID, err)
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Body:       "Error processing message",
		}, nil
	}

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Body:       "OK",
	}, nil
}

func main() {
	lambda.Start(handler)
}
