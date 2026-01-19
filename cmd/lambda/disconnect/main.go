package main

import (
	"context"
	"log"
	"os"

	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

var (
	store *storage.DynamoDBStore
)

func init() {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("Failed to load AWS config: %v", err)
	}
	ddbClient := dynamodb.NewFromConfig(cfg)
	store = storage.NewDynamoDBStore(ddbClient)
}

func handler(ctx context.Context, event events.APIGatewayWebsocketProxyRequest) (events.APIGatewayProxyResponse, error) {
	connectionID := event.RequestContext.ConnectionID

	log.Printf("Client disconnected: %s", connectionID)

	// Remove connection from DynamoDB
	if err := store.DeleteConnection(ctx, connectionID); err != nil {
		log.Printf("Failed to delete connection %s: %v", connectionID, err)
		// Don't fail the disconnect
	}

	return events.APIGatewayProxyResponse{
		StatusCode: 200,
		Body:       "Disconnected",
	}, nil
}

func main() {
	_ = os.Getenv("CONNECTIONS_TABLE")
	lambda.Start(handler)
}
