package observability

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// MetricName constants
const (
	MetricMessagesProcessed    = "MessagesProcessed"
	MetricMessagesFailed       = "MessagesFailed"
	MetricConnectionsActive    = "ConnectionsActive"
	MetricConnectionsNew       = "ConnectionsNew"
	MetricConnectionsClosed    = "ConnectionsClosed"
	MetricReportJobsQueued     = "ReportJobsQueued"
	MetricReportJobsCompleted  = "ReportJobsCompleted"
	MetricReportJobsFailed     = "ReportJobsFailed"
	MetricReportJobDuration    = "ReportJobDurationMs"
	MetricAPILatency           = "APILatencyMs"
	MetricDynamoDBLatency      = "DynamoDBLatencyMs"
	MetricS3Latency            = "S3LatencyMs"
	MetricSQSLatency           = "SQSLatencyMs"
	MetricWebSocketSendLatency = "WebSocketSendLatencyMs"
	MetricImageUploads         = "ImageUploads"
	MetricImageDownloads       = "ImageDownloads"
	MetricAnnouncementsSent    = "AnnouncementsSent"
	MetricErrorsTotal          = "ErrorsTotal"
	MetricWorkerActiveJobs     = "WorkerActiveJobs"
	MetricWorkerQueueDepth     = "WorkerQueueDepth"
)

// Metrics collects and publishes CloudWatch metrics
type Metrics struct {
	client    *cloudwatch.Client
	namespace string
	env       string

	// Buffered metrics for batch publishing
	buffer    []types.MetricDatum
	bufferMu  sync.Mutex

	// In-memory counters for high-frequency metrics
	counters  sync.Map // map[string]*int64

	// Flush interval
	flushTicker *time.Ticker
	done        chan struct{}
}

// NewMetrics creates a new metrics collector
func NewMetrics(client *cloudwatch.Client, namespace, env string) *Metrics {
	m := &Metrics{
		client:    client,
		namespace: namespace,
		env:       env,
		buffer:    make([]types.MetricDatum, 0, 20),
		done:      make(chan struct{}),
	}

	// Start background flusher
	m.flushTicker = time.NewTicker(60 * time.Second)
	go m.backgroundFlush()

	return m
}

// backgroundFlush periodically flushes metrics to CloudWatch
func (m *Metrics) backgroundFlush() {
	for {
		select {
		case <-m.flushTicker.C:
			m.Flush(context.Background())
		case <-m.done:
			return
		}
	}
}

// Stop stops the background flusher and flushes remaining metrics
func (m *Metrics) Stop(ctx context.Context) {
	close(m.done)
	m.flushTicker.Stop()
	m.Flush(ctx)
}

// Counter increments a counter metric
func (m *Metrics) Counter(name string, value int64) {
	v, _ := m.counters.LoadOrStore(name, new(int64))
	atomic.AddInt64(v.(*int64), value)
}

// Gauge records a gauge metric
func (m *Metrics) Gauge(ctx context.Context, name string, value float64, unit types.StandardUnit) {
	m.addMetric(name, value, unit, nil)
}

// Histogram records a histogram/timing metric
func (m *Metrics) Histogram(ctx context.Context, name string, value float64, unit types.StandardUnit) {
	m.addMetric(name, value, unit, nil)
}

// Latency records a latency metric in milliseconds
func (m *Metrics) Latency(ctx context.Context, name string, duration time.Duration) {
	m.addMetric(name, float64(duration.Milliseconds()), types.StandardUnitMilliseconds, nil)
}

// Timer returns a function to record latency
func (m *Metrics) Timer(ctx context.Context, name string) func() {
	start := time.Now()
	return func() {
		m.Latency(ctx, name, time.Since(start))
	}
}

// addMetric adds a metric to the buffer
func (m *Metrics) addMetric(name string, value float64, unit types.StandardUnit, dimensions []types.Dimension) {
	datum := types.MetricDatum{
		MetricName: aws.String(name),
		Value:      aws.Float64(value),
		Unit:       unit,
		Timestamp:  aws.Time(time.Now().UTC()),
		Dimensions: append(dimensions, types.Dimension{
			Name:  aws.String("Environment"),
			Value: aws.String(m.env),
		}),
	}

	m.bufferMu.Lock()
	m.buffer = append(m.buffer, datum)
	// Auto-flush if buffer is full (CloudWatch max is 1000)
	shouldFlush := len(m.buffer) >= 20
	m.bufferMu.Unlock()

	if shouldFlush {
		go m.Flush(context.Background())
	}
}

// Flush publishes all buffered metrics to CloudWatch
func (m *Metrics) Flush(ctx context.Context) error {
	// Collect counter metrics
	m.counters.Range(func(key, value interface{}) bool {
		name := key.(string)
		count := atomic.SwapInt64(value.(*int64), 0)
		if count > 0 {
			m.bufferMu.Lock()
			m.buffer = append(m.buffer, types.MetricDatum{
				MetricName: aws.String(name),
				Value:      aws.Float64(float64(count)),
				Unit:       types.StandardUnitCount,
				Timestamp:  aws.Time(time.Now().UTC()),
				Dimensions: []types.Dimension{
					{
						Name:  aws.String("Environment"),
						Value: aws.String(m.env),
					},
				},
			})
			m.bufferMu.Unlock()
		}
		return true
	})

	m.bufferMu.Lock()
	if len(m.buffer) == 0 {
		m.bufferMu.Unlock()
		return nil
	}

	// Take all metrics from buffer
	metrics := m.buffer
	m.buffer = make([]types.MetricDatum, 0, 20)
	m.bufferMu.Unlock()

	// Publish in batches of 20 (CloudWatch limit)
	for i := 0; i < len(metrics); i += 20 {
		end := i + 20
		if end > len(metrics) {
			end = len(metrics)
		}

		_, err := m.client.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
			Namespace:  aws.String(m.namespace),
			MetricData: metrics[i:end],
		})
		if err != nil {
			GetLogger().Error(ctx, "Failed to publish metrics", err, map[string]interface{}{
				"batch_size": end - i,
			})
			// Don't return error - metrics are best effort
		}
	}

	return nil
}

// RecordAPICall records metrics for an API call
func (m *Metrics) RecordAPICall(ctx context.Context, operation string, duration time.Duration, err error) {
	m.Latency(ctx, MetricAPILatency, duration)
	if err != nil {
		m.Counter(MetricErrorsTotal, 1)
	}
}

// RecordDynamoDBCall records metrics for a DynamoDB call
func (m *Metrics) RecordDynamoDBCall(ctx context.Context, operation string, duration time.Duration, err error) {
	m.Latency(ctx, MetricDynamoDBLatency, duration)
}

// RecordMessageProcessed records a processed message
func (m *Metrics) RecordMessageProcessed(success bool) {
	if success {
		m.Counter(MetricMessagesProcessed, 1)
	} else {
		m.Counter(MetricMessagesFailed, 1)
	}
}

// RecordConnection records connection events
func (m *Metrics) RecordConnection(event string) {
	switch event {
	case "new":
		m.Counter(MetricConnectionsNew, 1)
	case "closed":
		m.Counter(MetricConnectionsClosed, 1)
	}
}

// RecordReportJob records report job events
func (m *Metrics) RecordReportJob(event string, durationMs int64) {
	switch event {
	case "queued":
		m.Counter(MetricReportJobsQueued, 1)
	case "completed":
		m.Counter(MetricReportJobsCompleted, 1)
		if durationMs > 0 {
			m.Histogram(context.Background(), MetricReportJobDuration, float64(durationMs), types.StandardUnitMilliseconds)
		}
	case "failed":
		m.Counter(MetricReportJobsFailed, 1)
	}
}

// Health check metrics
type HealthStatus struct {
	Status      string            `json:"status"`
	Timestamp   time.Time         `json:"timestamp"`
	Version     string            `json:"version"`
	Checks      map[string]bool   `json:"checks"`
	Metrics     map[string]int64  `json:"metrics,omitempty"`
}

// GetHealthStatus returns current health status
func (m *Metrics) GetHealthStatus(version string, checks map[string]bool) HealthStatus {
	status := "healthy"
	for _, ok := range checks {
		if !ok {
			status = "unhealthy"
			break
		}
	}

	// Collect current counter values
	metrics := make(map[string]int64)
	m.counters.Range(func(key, value interface{}) bool {
		metrics[key.(string)] = atomic.LoadInt64(value.(*int64))
		return true
	})

	return HealthStatus{
		Status:    status,
		Timestamp: time.Now().UTC(),
		Version:   version,
		Checks:    checks,
		Metrics:   metrics,
	}
}
