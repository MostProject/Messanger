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
	"sync/atomic"
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
	AIClientID        = 9999
)

type Worker struct {
	store       *storage.DynamoDBStore
	s3Store     *storage.S3Store
	queue       *queue.SQSQueue
	wsService   *services.WebSocketService
	apiEndpoint string
	activeJobs  int32 // atomic counter for active jobs
}

func NewWorker() (*Worker, error) {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Validate required environment variables
	apiEndpoint := os.Getenv("WEBSOCKET_API_ENDPOINT")
	if apiEndpoint == "" {
		return nil, fmt.Errorf("WEBSOCKET_API_ENDPOINT environment variable is required")
	}

	queueURL := os.Getenv("REPORT_QUEUE_URL")
	if queueURL == "" {
		return nil, fmt.Errorf("REPORT_QUEUE_URL environment variable is required")
	}

	anthropicKey := os.Getenv("ANTHROPIC_API_KEY")
	if anthropicKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
	}

	ddbClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)

	apiClient := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = &apiEndpoint
	})

	store := storage.NewDynamoDBStore(ddbClient)
	wsService := services.NewWebSocketService(apiClient, store)

	return &Worker{
		store:       store,
		s3Store:     storage.NewS3Store(s3Client),
		queue:       queue.NewSQSQueue(sqsClient, queueURL),
		wsService:   wsService,
		apiEndpoint: apiEndpoint,
		activeJobs:  0,
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
			log.Println("Shutdown signal received, waiting for in-flight jobs...")
			// Wait for active jobs to complete with timeout
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for atomic.LoadInt32(&w.activeJobs) > 0 {
				select {
				case <-shutdownCtx.Done():
					log.Printf("Shutdown timeout, %d jobs still active", atomic.LoadInt32(&w.activeJobs))
					return nil
				case <-time.After(500 * time.Millisecond):
					// Check again
				}
			}
			log.Println("All jobs completed")
			return nil
		case <-ticker.C:
			w.pollAndProcess(ctx)
		}
	}
}

func (w *Worker) pollAndProcess(ctx context.Context) {
	// Use atomic counter to check available slots (no race condition)
	currentJobs := atomic.LoadInt32(&w.activeJobs)
	availableSlots := int32(maxConcurrentJobs) - currentJobs
	if availableSlots <= 0 {
		return
	}

	jobs, receiptHandles, err := w.queue.ReceiveReportJobs(ctx, availableSlots)
	if err != nil {
		log.Printf("Error receiving jobs: %v", err)
		return
	}

	for i, job := range jobs {
		// Double-check we haven't exceeded limit (another goroutine might have started)
		if atomic.LoadInt32(&w.activeJobs) >= int32(maxConcurrentJobs) {
			log.Printf("Max concurrent jobs reached, deferring remaining %d jobs", len(jobs)-i)
			return
		}

		receiptHandle := receiptHandles[i]

		// Increment active jobs counter atomically
		atomic.AddInt32(&w.activeJobs, 1)

		go func(j *models.ReportJob, rh string) {
			defer atomic.AddInt32(&w.activeJobs, -1)
			w.processJob(ctx, j, rh)
		}(job, receiptHandle)
	}
}

func (w *Worker) processJob(ctx context.Context, job *models.ReportJob, receiptHandle string) {
	startTime := time.Now().UTC()
	log.Printf("Processing job %s for user %d", job.JobID, job.UserID)

	// Update job status
	job.Status = models.JobStatusProcessing
	job.StartedAt = &startTime
	if err := w.store.SaveReportJob(ctx, job); err != nil {
		log.Printf("Warning: failed to save job status: %v", err)
	}

	// Send progress update (ignore errors - best effort)
	_ = w.wsService.SendReportPlaceholder(ctx, job.UserID, job.RequestID, "Processing with Claude Code", 30, "Analyzing query")

	// Execute Claude Code
	result, err := w.executeClaude(ctx, job)
	if err != nil {
		log.Printf("Job %s failed: %v", job.JobID, err)
		if err := w.store.UpdateReportJobStatus(ctx, job.JobID, models.JobStatusFailed, "", err.Error()); err != nil {
			log.Printf("Warning: failed to update job status: %v", err)
		}

		// Send error to user
		report := &models.ReportMessage{
			MesajGonderenKullaniciID: AIClientID,
			MesajAliciKullaniciID:    job.UserID,
			MesajGonderilenTarih:     time.Now().Format("2006-01-02 15:04:05"),
			RequestID:                job.RequestID,
			IsSuccessful:             false,
			ErrorMessage:             err.Error(),
			ProcessingTimeMs:         time.Since(startTime).Milliseconds(),
		}
		_ = w.wsService.SendReport(ctx, job.UserID, report)

		// Delete from queue
		if err := w.queue.DeleteMessage(ctx, receiptHandle); err != nil {
			log.Printf("Warning: failed to delete message from queue: %v", err)
		}
		return
	}

	// Save result to S3
	reportKey, err := w.s3Store.SaveReport(ctx, job.JobID, result)
	if err != nil {
		log.Printf("Warning: failed to save report to S3: %v", err)
		// Continue anyway - we can still send the result
	}

	// Update job status
	if err := w.store.UpdateReportJobStatus(ctx, job.JobID, models.JobStatusCompleted, reportKey, ""); err != nil {
		log.Printf("Warning: failed to update job status: %v", err)
	}

	// Parse and send result to user
	report := w.parseClaudeResult(result, job, startTime)
	if err := w.wsService.SendReport(ctx, job.UserID, report); err != nil {
		log.Printf("Warning: failed to send report to user: %v", err)
	}

	// Delete from queue
	if err := w.queue.DeleteMessage(ctx, receiptHandle); err != nil {
		log.Printf("Warning: failed to delete message from queue: %v", err)
	}

	log.Printf("Job %s completed successfully in %v", job.JobID, time.Since(startTime))
}

func (w *Worker) executeClaude(ctx context.Context, job *models.ReportJob) (string, error) {
	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	// Build prompt for Claude Code
	prompt := w.buildPrompt(job)

	// Execute Claude Code CLI
	cmd := exec.CommandContext(execCtx, "claude", "-p", prompt, "--output-format", "json")

	// Set environment variables (inherit current environment)
	cmd.Env = os.Environ()

	// Capture output
	output, err := cmd.CombinedOutput()
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude code execution timed out after %v", jobTimeout)
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("worker shutdown requested")
		}
		// Truncate output for error message
		outputStr := string(output)
		if len(outputStr) > 500 {
			outputStr = outputStr[:500] + "..."
		}
		return "", fmt.Errorf("claude code execution failed: %w\nOutput: %s", err, outputStr)
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

func (w *Worker) parseClaudeResult(result string, job *models.ReportJob, startTime time.Time) *models.ReportMessage {
	report := &models.ReportMessage{
		MesajGonderenKullaniciID: AIClientID,
		MesajAliciKullaniciID:    job.UserID,
		MesajGonderilenTarih:     time.Now().Format("2006-01-02 15:04:05"),
		RequestID:                job.RequestID,
		ProcessingTimeMs:         time.Since(startTime).Milliseconds(),
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
		// If not valid JSON, treat raw result as HTML (might be direct output)
		log.Printf("Warning: could not parse Claude output as JSON: %v", err)
		report.ReportQuery = job.Query
		report.WebApplication = result
		// Mark as successful only if we have meaningful content
		report.IsSuccessful = len(result) > 100
		if !report.IsSuccessful {
			report.ErrorMessage = "Invalid response format from Claude"
		}
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
		sig := <-sigCh
		log.Printf("Received signal: %v", sig)
		cancel()
	}()

	if err := worker.Run(ctx); err != nil {
		log.Fatalf("Worker error: %v", err)
	}

	log.Println("Worker shutdown complete")
}
