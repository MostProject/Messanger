package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/MostProject/Messanger/internal/models"
	"github.com/MostProject/Messanger/internal/queue"
	"github.com/MostProject/Messanger/internal/services"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const (
	maxConcurrentJobs = 3
	pollInterval      = 5 * time.Second
	jobTimeout        = 5 * time.Minute
)

type Worker struct {
	store     *storage.DynamoDBStore
	s3Store   *storage.S3Store
	queue     *queue.SQSQueue
	wsService *services.WebSocketService
	apiEndpoint string
	semaphore chan struct{}
}

func NewWorker() (*Worker, error) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	ddbClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)

	apiEndpoint := os.Getenv("WEBSOCKET_API_ENDPOINT")
	apiClient := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = &apiEndpoint
	})

	store := storage.NewDynamoDBStore(ddbClient)
	wsService := services.NewWebSocketService(apiClient, store)

	return &Worker{
		store:       store,
		s3Store:     storage.NewS3Store(s3Client),
		queue:       queue.NewSQSQueue(sqsClient, os.Getenv("REPORT_QUEUE_URL")),
		wsService:   wsService,
		apiEndpoint: apiEndpoint,
		semaphore:   make(chan struct{}, maxConcurrentJobs),
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	log.Println("Claude Code Worker started")
	log.Printf("API Endpoint: %s", w.apiEndpoint)
	log.Printf("Max concurrent jobs: %d", maxConcurrentJobs)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutdown signal received, draining jobs...")
			// Wait for in-flight jobs to complete
			for i := 0; i < maxConcurrentJobs; i++ {
				w.semaphore <- struct{}{}
			}
			return nil
		case <-ticker.C:
			w.pollAndProcess(ctx)
		}
	}
}

func (w *Worker) pollAndProcess(ctx context.Context) {
	// Check how many slots are available
	availableSlots := maxConcurrentJobs - len(w.semaphore)
	if availableSlots <= 0 {
		return
	}

	jobs, receiptHandles, err := w.queue.ReceiveReportJobs(ctx, int32(availableSlots))
	if err != nil {
		log.Printf("Error receiving jobs: %v", err)
		return
	}

	for i, job := range jobs {
		receiptHandle := receiptHandles[i]

		// Acquire semaphore
		select {
		case w.semaphore <- struct{}{}:
			go func(j *models.ReportJob, rh string) {
				defer func() { <-w.semaphore }()
				w.processJob(ctx, j, rh)
			}(job, receiptHandle)
		default:
			// No slots available, job will be reprocessed later
			return
		}
	}
}

func (w *Worker) processJob(ctx context.Context, job *models.ReportJob, receiptHandle string) {
	log.Printf("Processing job %s for user %d", job.JobID, job.UserID)

	// Update job status
	now := time.Now().UTC()
	job.Status = models.JobStatusProcessing
	job.StartedAt = &now
	w.store.SaveReportJob(ctx, job)

	// Send progress update
	w.wsService.SendReportPlaceholder(ctx, job.UserID, job.RequestID, "Processing with Claude Code", 30, "Analyzing query")

	// Execute Claude Code
	result, err := w.executeClaude(ctx, job)
	if err != nil {
		log.Printf("Job %s failed: %v", job.JobID, err)
		w.store.UpdateReportJobStatus(ctx, job.JobID, models.JobStatusFailed, "", err.Error())

		// Send error to user
		report := &models.ReportMessage{
			MesajGonderenKullaniciID: 9999,
			MesajAliciKullaniciID:    job.UserID,
			RequestID:                job.RequestID,
			IsSuccessful:             false,
			ErrorMessage:             err.Error(),
		}
		w.wsService.SendReport(ctx, job.UserID, report)

		// Delete from queue
		w.queue.DeleteMessage(ctx, receiptHandle)
		return
	}

	// Save result to S3
	reportKey, err := w.s3Store.SaveReport(ctx, job.JobID, result)
	if err != nil {
		log.Printf("Failed to save report to S3: %v", err)
	}

	// Update job status
	w.store.UpdateReportJobStatus(ctx, job.JobID, models.JobStatusCompleted, reportKey, "")

	// Parse and send result to user
	report := w.parseClaudeResult(result, job)
	w.wsService.SendReport(ctx, job.UserID, report)

	// Delete from queue
	w.queue.DeleteMessage(ctx, receiptHandle)

	log.Printf("Job %s completed successfully", job.JobID)
}

func (w *Worker) executeClaude(ctx context.Context, job *models.ReportJob) (string, error) {
	// Create context with timeout
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	// Build prompt for Claude Code
	prompt := w.buildPrompt(job)

	// Execute Claude Code CLI
	// The -p flag is for prompt, --output-format json for structured output
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--output-format", "json")

	// Set environment variables
	cmd.Env = append(os.Environ(),
		"ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"),
	)

	// Capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude code execution timed out after %v", jobTimeout)
		}
		return "", fmt.Errorf("claude code execution failed: %w\nOutput: %s", err, string(output))
	}

	return string(output), nil
}

func (w *Worker) buildPrompt(job *models.ReportJob) string {
	var sb strings.Builder

	sb.WriteString("You are a report generation assistant for a jewelry store management system.\n\n")

	if job.Schema != "" {
		sb.WriteString("Database Schema:\n")
		sb.WriteString(job.Schema)
		sb.WriteString("\n\n")
	}

	sb.WriteString("User Request:\n")
	sb.WriteString(job.Query)
	sb.WriteString("\n\n")

	sb.WriteString(`Generate a report based on the user's request. Return the response in JSON format with the following structure:
{
  "reportQuery": "A summary of what the report shows",
  "requiredTables": "List of database tables needed for this report",
  "webApplication": "HTML/JavaScript code for displaying the report",
  "isSuccessful": true
}

The webApplication should be a complete, self-contained HTML page that displays the report data in a professional, user-friendly format suitable for a jewelry store business.
`)

	return sb.String()
}

func (w *Worker) parseClaudeResult(result string, job *models.ReportJob) *models.ReportMessage {
	report := &models.ReportMessage{
		MesajGonderenKullaniciID: 9999, // AI user ID
		MesajAliciKullaniciID:    job.UserID,
		MesajGonderilenTarih:     time.Now().Format("2006-01-02 15:04:05"),
		RequestID:                job.RequestID,
		ProcessingTimeMs:         time.Since(*job.StartedAt).Milliseconds(),
	}

	// Try to parse as JSON
	var parsed struct {
		ReportQuery    string `json:"reportQuery"`
		RequiredTables string `json:"requiredTables"`
		WebApplication string `json:"webApplication"`
		IsSuccessful   bool   `json:"isSuccessful"`
		ErrorMessage   string `json:"errorMessage,omitempty"`
	}

	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		// If not valid JSON, use raw result
		report.ReportQuery = job.Query
		report.WebApplication = result
		report.IsSuccessful = true
	} else {
		report.ReportQuery = parsed.ReportQuery
		report.RequiredTables = parsed.RequiredTables
		report.WebApplication = parsed.WebApplication
		report.IsSuccessful = parsed.IsSuccessful
		report.ErrorMessage = parsed.ErrorMessage
	}

	return report
}

func main() {
	worker, err := NewWorker()
	if err != nil {
		log.Fatalf("Failed to create worker: %v", err)
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Received shutdown signal")
		cancel()
	}()

	if err := worker.Run(ctx); err != nil {
		log.Fatalf("Worker error: %v", err)
	}

	log.Println("Worker shutdown complete")
}
