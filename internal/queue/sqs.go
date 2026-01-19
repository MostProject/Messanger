package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MostProject/Messanger/internal/models"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const (
	ReportQueueURL = "https://sqs.us-east-1.amazonaws.com/ACCOUNT_ID/messanger-report-jobs"
)

// SQSQueue handles SQS operations
type SQSQueue struct {
	client   *sqs.Client
	queueURL string
}

// NewSQSQueue creates a new SQS queue handler
func NewSQSQueue(client *sqs.Client, queueURL string) *SQSQueue {
	if queueURL == "" {
		queueURL = ReportQueueURL
	}
	return &SQSQueue{
		client:   client,
		queueURL: queueURL,
	}
}

// EnqueueReportJob sends a report job to the queue
func (q *SQSQueue) EnqueueReportJob(ctx context.Context, job *models.ReportJob) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}

	_, err = q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(q.queueURL),
		MessageBody: aws.String(string(body)),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"JobId": {
				DataType:    aws.String("String"),
				StringValue: aws.String(job.JobID),
			},
			"UserId": {
				DataType:    aws.String("Number"),
				StringValue: aws.String(fmt.Sprintf("%d", job.UserID)),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send message to queue: %w", err)
	}

	return nil
}

// ReceiveReportJobs receives report jobs from the queue (used by worker)
func (q *SQSQueue) ReceiveReportJobs(ctx context.Context, maxMessages int32) ([]*models.ReportJob, []string, error) {
	result, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(q.queueURL),
		MaxNumberOfMessages: maxMessages,
		WaitTimeSeconds:     20, // Long polling
		VisibilityTimeout:   300, // 5 minutes to process
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to receive messages: %w", err)
	}

	jobs := make([]*models.ReportJob, 0, len(result.Messages))
	receiptHandles := make([]string, 0, len(result.Messages))

	for _, msg := range result.Messages {
		var job models.ReportJob
		if err := json.Unmarshal([]byte(*msg.Body), &job); err != nil {
			continue // Skip invalid messages
		}
		jobs = append(jobs, &job)
		receiptHandles = append(receiptHandles, *msg.ReceiptHandle)
	}

	return jobs, receiptHandles, nil
}

// DeleteMessage removes a processed message from the queue
func (q *SQSQueue) DeleteMessage(ctx context.Context, receiptHandle string) error {
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(q.queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	})
	return err
}

// ChangeMessageVisibility extends or resets message visibility timeout
func (q *SQSQueue) ChangeMessageVisibility(ctx context.Context, receiptHandle string, timeoutSeconds int32) error {
	_, err := q.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(q.queueURL),
		ReceiptHandle:     aws.String(receiptHandle),
		VisibilityTimeout: timeoutSeconds,
	})
	return err
}
