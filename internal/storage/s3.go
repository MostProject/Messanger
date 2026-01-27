package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// Bucket names from environment variables
var (
	ImagesBucket  = getEnvOrDefaultS3("IMAGES_BUCKET", "messanger-images")
	ReportsBucket = getEnvOrDefaultS3("REPORTS_BUCKET", "messanger-reports")
)

const (
	MaxImageSizeBytes    = 5 * 1024 * 1024 // 5MB
	PresignedURLDuration = 15 * time.Minute
)

func getEnvOrDefaultS3(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// S3Store handles S3 operations
type S3Store struct {
	client       *s3.Client
	presignClient *s3.PresignClient
}

// NewS3Store creates a new S3 store
func NewS3Store(client *s3.Client) *S3Store {
	return &S3Store{
		client:       client,
		presignClient: s3.NewPresignClient(client),
	}
}

// UploadImage uploads a base64-encoded image to S3
func (s *S3Store) UploadImage(ctx context.Context, base64Data string, senderID, recipientID int) (string, error) {
	// Decode base64
	imageData, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return "", fmt.Errorf("invalid base64 image data: %w", err)
	}

	// Check size
	if len(imageData) > MaxImageSizeBytes {
		return "", fmt.Errorf("image size exceeds maximum allowed (%d bytes)", MaxImageSizeBytes)
	}

	// Generate unique key
	timestamp := time.Now().Format("20060102")
	imageID := uuid.New().String()
	key := fmt.Sprintf("%s/%d_%d_%s.jpg", timestamp, senderID, recipientID, imageID)

	// Upload to S3
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(ImagesBucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(imageData),
		ContentType: aws.String("image/jpeg"),
	})
	if err != nil {
		return "", fmt.Errorf("failed to upload image: %w", err)
	}

	return key, nil
}

// GetImagePresignedURL generates a presigned URL for downloading an image
func (s *S3Store) GetImagePresignedURL(ctx context.Context, key string) (string, error) {
	presignedReq, err := s.presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(ImagesBucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = PresignedURLDuration
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return presignedReq.URL, nil
}

// GetUploadPresignedURL generates a presigned URL for uploading an image
func (s *S3Store) GetUploadPresignedURL(ctx context.Context, senderID, recipientID int) (string, string, error) {
	timestamp := time.Now().Format("20060102")
	imageID := uuid.New().String()
	key := fmt.Sprintf("%s/%d_%d_%s.jpg", timestamp, senderID, recipientID, imageID)

	presignedReq, err := s.presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(ImagesBucket),
		Key:         aws.String(key),
		ContentType: aws.String("image/jpeg"),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = PresignedURLDuration
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to generate upload URL: %w", err)
	}

	return presignedReq.URL, key, nil
}

// SaveReport saves a generated report to S3
func (s *S3Store) SaveReport(ctx context.Context, jobID string, reportContent string) (string, error) {
	key := fmt.Sprintf("reports/%s/%s.json", time.Now().Format("2006/01/02"), jobID)

	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(ReportsBucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader([]byte(reportContent)),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return "", fmt.Errorf("failed to save report: %w", err)
	}

	return key, nil
}

// GetReport retrieves a report from S3
func (s *S3Store) GetReport(ctx context.Context, key string) (string, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(ReportsBucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("failed to get report: %w", err)
	}
	defer result.Body.Close()

	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(result.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read report: %w", err)
	}

	return buf.String(), nil
}
