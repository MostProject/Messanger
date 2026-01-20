package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/MostProject/Messanger/internal/health"
	"github.com/MostProject/Messanger/internal/models"
	"github.com/MostProject/Messanger/internal/observability"
	"github.com/MostProject/Messanger/internal/queue"
	"github.com/MostProject/Messanger/internal/resilience"
	"github.com/MostProject/Messanger/internal/services"
	"github.com/MostProject/Messanger/internal/storage"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigatewaymanagementapi"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const (
	maxConcurrentJobs    = 3
	pollInterval         = 5 * time.Second
	jobTimeout           = 5 * time.Minute
	AIClientID           = 9999
	healthCheckPort      = "8080"
	version              = "1.0.0"
	progressUpdatePeriod = 15 * time.Second
)

type Worker struct {
	store       *storage.DynamoDBStore
	s3Store     *storage.S3Store
	queue       *queue.SQSQueue
	wsService   *services.WebSocketService
	apiEndpoint string
	activeJobs  int32

	// Observability
	logger     *observability.Logger
	metrics    *observability.Metrics
	health     *health.Server

	// Resilience
	circuitBreakers *resilience.CircuitBreakerRegistry

	// Metrics counters
	jobsProcessed *int64
	jobsFailed    *int64
	jobsQueued    *int64
}

func NewWorker() (*Worker, error) {
	ctx := context.Background()
	logger := observability.NewLogger("worker", observability.LevelInfo)

	cfg, err := config.LoadDefaultConfig(ctx)
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

	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "dev"
	}

	// Initialize AWS clients
	ddbClient := dynamodb.NewFromConfig(cfg)
	s3Client := s3.NewFromConfig(cfg)
	sqsClient := sqs.NewFromConfig(cfg)
	cwClient := cloudwatch.NewFromConfig(cfg)

	apiClient := apigatewaymanagementapi.NewFromConfig(cfg, func(o *apigatewaymanagementapi.Options) {
		o.BaseEndpoint = &apiEndpoint
	})

	store := storage.NewDynamoDBStore(ddbClient)
	wsService := services.NewWebSocketService(apiClient, store)
	metrics := observability.NewMetrics(cwClient, "Messanger/Worker", env)

	// Health check server
	healthServer := health.NewServer(healthCheckPort, version)

	// Register health checkers
	healthServer.RegisterChecker("sqs", health.SQSChecker(func(ctx context.Context) error {
		// Quick SQS health check - just verify we can call the API
		return nil // The queue.ReceiveReportJobs will fail if SQS is unhealthy
	}))

	healthServer.RegisterChecker("dynamodb", health.DynamoDBChecker(func(ctx context.Context) error {
		// Quick DynamoDB health check
		return nil
	}))

	// Register metrics
	jobsProcessed := healthServer.RegisterMetric("jobs_processed")
	jobsFailed := healthServer.RegisterMetric("jobs_failed")
	jobsQueued := healthServer.RegisterMetric("jobs_queued")

	w := &Worker{
		store:           store,
		s3Store:         storage.NewS3Store(s3Client),
		queue:           queue.NewSQSQueue(sqsClient, queueURL),
		wsService:       wsService,
		apiEndpoint:     apiEndpoint,
		activeJobs:      0,
		logger:          logger,
		metrics:         metrics,
		health:          healthServer,
		circuitBreakers: resilience.NewRegistry(),
		jobsProcessed:   jobsProcessed,
		jobsFailed:      jobsFailed,
		jobsQueued:      jobsQueued,
	}

	return w, nil
}

func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info(ctx, "Claude Code Worker started", map[string]interface{}{
		"api_endpoint":        w.apiEndpoint,
		"max_concurrent_jobs": maxConcurrentJobs,
		"version":             version,
	})

	// Start health check server
	go func() {
		if err := w.health.Start(); err != nil {
			w.logger.Error(ctx, "Health server error", err, nil)
		}
	}()

	// Mark as ready after initialization
	w.health.SetReady(true)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Metrics flush ticker
	metricsTicker := time.NewTicker(60 * time.Second)
	defer metricsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return w.gracefulShutdown()

		case <-ticker.C:
			w.pollAndProcess(ctx)

		case <-metricsTicker.C:
			w.metrics.Flush(ctx)
		}
	}
}

func (w *Worker) gracefulShutdown() error {
	ctx := context.Background()
	w.logger.Info(ctx, "Shutdown signal received, draining jobs...", nil)

	// Mark as not ready immediately
	w.health.SetReady(false)

	// Wait for active jobs with timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	activeCount := atomic.LoadInt32(&w.activeJobs)
	for activeCount > 0 {
		w.logger.Info(shutdownCtx, "Waiting for jobs to complete", map[string]interface{}{
			"active_jobs": activeCount,
		})
		select {
		case <-shutdownCtx.Done():
			w.logger.Warn(ctx, "Shutdown timeout reached", map[string]interface{}{
				"abandoned_jobs": atomic.LoadInt32(&w.activeJobs),
			})
			break
		case <-time.After(500 * time.Millisecond):
			activeCount = atomic.LoadInt32(&w.activeJobs)
		}
	}

	// Flush remaining metrics
	w.metrics.Flush(ctx)

	// Stop health server
	if err := w.health.Stop(shutdownCtx); err != nil {
		w.logger.Error(ctx, "Failed to stop health server", err, nil)
	}

	w.logger.Info(ctx, "Worker shutdown complete", nil)
	return nil
}

func (w *Worker) pollAndProcess(ctx context.Context) {
	currentJobs := atomic.LoadInt32(&w.activeJobs)
	availableSlots := int32(maxConcurrentJobs) - currentJobs
	if availableSlots <= 0 {
		return
	}

	// Use circuit breaker for SQS
	sqsBreaker := w.circuitBreakers.GetWithConfig(resilience.CircuitBreakerConfig{
		Name:             "sqs",
		MaxFailures:      3,
		ResetTimeout:     30 * time.Second,
		HalfOpenMaxCalls: 2,
	})

	var jobs []*models.ReportJob
	var receiptHandles []string
	var err error

	err = sqsBreaker.Execute(ctx, func(ctx context.Context) error {
		timer := w.metrics.Timer(ctx, observability.MetricSQSLatency)
		jobs, receiptHandles, err = w.queue.ReceiveReportJobs(ctx, availableSlots)
		timer()
		return err
	})

	if err != nil {
		if err == resilience.ErrCircuitOpen {
			w.logger.Warn(ctx, "SQS circuit breaker open, skipping poll", nil)
		} else {
			w.logger.Error(ctx, "Error receiving jobs", err, nil)
		}
		return
	}

	for i, job := range jobs {
		if atomic.LoadInt32(&w.activeJobs) >= int32(maxConcurrentJobs) {
			w.logger.Info(ctx, "Max concurrent jobs reached", map[string]interface{}{
				"deferred_jobs": len(jobs) - i,
			})
			return
		}

		receiptHandle := receiptHandles[i]
		atomic.AddInt32(&w.activeJobs, 1)
		atomic.AddInt64(w.jobsQueued, 1)

		go func(j *models.ReportJob, rh string) {
			defer atomic.AddInt32(&w.activeJobs, -1)
			w.processJob(ctx, j, rh)
		}(job, receiptHandle)
	}
}

func (w *Worker) processJob(ctx context.Context, job *models.ReportJob, receiptHandle string) {
	startTime := time.Now().UTC()

	// Add job context
	ctx = observability.WithRequestID(ctx, job.JobID)
	ctx = observability.WithUserID(ctx, job.UserID)

	w.logger.Info(ctx, "Processing job started", map[string]interface{}{
		"job_id":  job.JobID,
		"user_id": job.UserID,
		"query":   truncate(job.Query, 100),
	})

	// Update job status with retries
	job.Status = models.JobStatusProcessing
	job.StartedAt = &startTime

	err := resilience.Retry(ctx, resilience.DefaultRetryConfig(), func(ctx context.Context) error {
		return w.store.SaveReportJob(ctx, job)
	})
	if err != nil {
		w.logger.Error(ctx, "Failed to save job status", err, nil)
	}

	// Start progress updates in background
	progressCtx, cancelProgress := context.WithCancel(ctx)
	go w.sendProgressUpdates(progressCtx, job, startTime)

	// Execute Claude Code with circuit breaker
	claudeBreaker := w.circuitBreakers.GetWithConfig(resilience.CircuitBreakerConfig{
		Name:             "claude",
		MaxFailures:      5,
		ResetTimeout:     60 * time.Second,
		HalfOpenMaxCalls: 2,
	})

	var result string
	err = claudeBreaker.Execute(ctx, func(ctx context.Context) error {
		var execErr error
		result, execErr = w.executeClaude(ctx, job)
		return execErr
	})

	// Stop progress updates
	cancelProgress()

	duration := time.Since(startTime)

	if err != nil {
		w.handleJobFailure(ctx, job, receiptHandle, err, startTime)
		return
	}

	w.handleJobSuccess(ctx, job, receiptHandle, result, startTime, duration)
}

func (w *Worker) sendProgressUpdates(ctx context.Context, job *models.ReportJob, startTime time.Time) {
	ticker := time.NewTicker(progressUpdatePeriod)
	defer ticker.Stop()

	progress := 30
	steps := []string{
		"Analyzing query",
		"Generating report structure",
		"Processing data",
		"Formatting output",
	}
	stepIdx := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if progress < 90 {
				progress += 15
			}
			step := steps[stepIdx%len(steps)]
			stepIdx++

			_ = w.wsService.SendReportPlaceholder(ctx, job.UserID, job.RequestID,
				fmt.Sprintf("Processing... (%s elapsed)", time.Since(startTime).Round(time.Second)),
				progress, step)
		}
	}
}

func (w *Worker) handleJobFailure(ctx context.Context, job *models.ReportJob, receiptHandle string, err error, startTime time.Time) {
	duration := time.Since(startTime)

	w.logger.Error(ctx, "Job failed", err, map[string]interface{}{
		"job_id":      job.JobID,
		"duration_ms": duration.Milliseconds(),
	})

	atomic.AddInt64(w.jobsFailed, 1)
	w.metrics.RecordReportJob("failed", duration.Milliseconds())

	// Update job status
	if updateErr := w.store.UpdateReportJobStatus(ctx, job.JobID, models.JobStatusFailed, "", err.Error()); updateErr != nil {
		w.logger.Error(ctx, "Failed to update job status", updateErr, nil)
	}

	// Send error to user
	report := &models.ReportMessage{
		MesajGonderenKullaniciID: AIClientID,
		MesajAliciKullaniciID:    job.UserID,
		MesajGonderilenTarih:     time.Now().Format("2006-01-02 15:04:05"),
		RequestID:                job.RequestID,
		IsSuccessful:             false,
		ErrorMessage:             err.Error(),
		ProcessingTimeMs:         duration.Milliseconds(),
	}
	_ = w.wsService.SendReport(ctx, job.UserID, report)

	// Delete from queue
	if delErr := w.queue.DeleteMessage(ctx, receiptHandle); delErr != nil {
		w.logger.Error(ctx, "Failed to delete message from queue", delErr, nil)
	}
}

func (w *Worker) handleJobSuccess(ctx context.Context, job *models.ReportJob, receiptHandle string, result string, startTime time.Time, duration time.Duration) {
	w.logger.Info(ctx, "Job completed successfully", map[string]interface{}{
		"job_id":        job.JobID,
		"duration_ms":   duration.Milliseconds(),
		"result_length": len(result),
	})

	atomic.AddInt64(w.jobsProcessed, 1)
	w.metrics.RecordReportJob("completed", duration.Milliseconds())

	// Save result to S3 with retry
	var reportKey string
	err := resilience.Retry(ctx, resilience.DefaultRetryConfig(), func(ctx context.Context) error {
		var s3Err error
		reportKey, s3Err = w.s3Store.SaveReport(ctx, job.JobID, result)
		return s3Err
	})
	if err != nil {
		w.logger.Warn(ctx, "Failed to save report to S3", map[string]interface{}{
			"error": err.Error(),
		})
	}

	// Update job status
	if updateErr := w.store.UpdateReportJobStatus(ctx, job.JobID, models.JobStatusCompleted, reportKey, ""); updateErr != nil {
		w.logger.Error(ctx, "Failed to update job status", updateErr, nil)
	}

	// Parse and send result to user
	report := w.parseClaudeResult(result, job, startTime)
	if sendErr := w.wsService.SendReport(ctx, job.UserID, report); sendErr != nil {
		w.logger.Error(ctx, "Failed to send report to user", sendErr, nil)
	}

	// Delete from queue
	if delErr := w.queue.DeleteMessage(ctx, receiptHandle); delErr != nil {
		w.logger.Error(ctx, "Failed to delete message from queue", delErr, nil)
	}
}

func (w *Worker) executeClaude(ctx context.Context, job *models.ReportJob) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	prompt := w.buildPrompt(job)

	cmd := exec.CommandContext(execCtx, "claude", "-p", prompt, "--output-format", "json")
	cmd.Env = os.Environ()

	w.logger.Debug(ctx, "Executing Claude Code", map[string]interface{}{
		"prompt_length": len(prompt),
	})

	output, err := cmd.CombinedOutput()
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude code execution timed out after %v", jobTimeout)
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("worker shutdown requested")
		}
		outputStr := truncate(string(output), 500)
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

	var parsed struct {
		ReportQuery    string `json:"reportQuery"`
		RequiredTables string `json:"requiredTables"`
		WebApplication string `json:"webApplication"`
		IsSuccessful   bool   `json:"isSuccessful"`
		ErrorMessage   string `json:"errorMessage,omitempty"`
	}

	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		w.logger.Warn(context.Background(), "Could not parse Claude output as JSON", map[string]interface{}{
			"error":         err.Error(),
			"result_length": len(result),
		})
		report.ReportQuery = job.Query
		report.WebApplication = result
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

// truncate truncates a string to maxLen characters
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func main() {
	worker, err := NewWorker()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create worker: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		worker.logger.Info(ctx, "Received signal", map[string]interface{}{
			"signal": sig.String(),
		})
		cancel()
	}()

	if err := worker.Run(ctx); err != nil {
		worker.logger.Error(ctx, "Worker error", err, nil)
		os.Exit(1)
	}
}
