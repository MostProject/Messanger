package main

import (
	"context"
	"log"
	"os"

	"github.com/MostProject/Messanger/internal/handlers"
	"github.com/MostProject/Messanger/internal/queue"
	"github.com/MostProject/Messanger/internal/services"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

var (
	messageHandler *handlers.MessageHandler
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}

	// Initialize clients
	ddbClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)

	// Initialize stores
	ddbStore := storage.NewDynamoDBStore(ddbClient)
	s3Store := storage.NewS3Store(s3Client)
	sqsQueue := queue.NewSQSQueue(sqsClient, os.Getenv("REPORT_QUEUE_URL"))

	// API Gateway Management client will be created per-request with the correct endpoint
	messageHandler = handlers.NewMessageHandler(ddbStore, s3Store, nil, sqsQueue)
}

func handler(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	connectionID := event.RequestContext.ConnectionID
	body := event.Body

	log.Printf("Message from %s: %s", connectionID, body)

	// Create API Gateway Management client with the correct endpoint
	cfg, _ := config.LoadDefaultConfig(ctx)
	endpoint := "https://" + event.RequestContext.DomainName + "/" + event.RequestContext.Stage
	apiClient := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = &endpoint
	})

	// Create WebSocket service with API client
	ddbClient := dynamodb.NewFromConfig(cfg)
	store := storage.NewDynamoDBStore(ddbClient)
	wsService := services.NewWebSocketService(apiClient, store)

	// Create new handler with WebSocket service
	s3Client := s3.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)
	s3Store := storage.NewS3Store(s3Client)
	sqsQueue := queue.NewSQSQueue(sqsClient, os.Getenv("REPORT_QUEUE_URL"))
	handler := handlers.NewMessageHandler(store, s3Store, wsService, sqsQueue)

	// Handle the message
	if err := handler.HandleMessage(ctx, connectionID, body); err != nil {
		log.Printf("Error handling message: %v", err)
		return events.APIGatewayProxyResponse{
			StatusCode: 500,
			Body:       "Error processing message",
		}, nil
	}

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Body:       "Message processed",
	}, nil
}

func main() {
	lambda.Start(handler)
}
