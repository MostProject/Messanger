package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Status represents health check status
type Status string

const (
	StatusHealthy   Status = "healthy"
	StatusDegraded  Status = "degraded"
	StatusUnhealthy Status = "unhealthy"
)

// Check represents a single health check
type Check struct {
	Name      string        `json:"name"`
	Status    Status        `json:"status"`
	Message   string        `json:"message,omitempty"`
	Duration  time.Duration `json:"duration_ms"`
	LastCheck time.Time     `json:"last_check"`
}

// Response represents the health check response
type Response struct {
	Status    Status           `json:"status"`
	Version   string           `json:"version"`
	Uptime    string           `json:"uptime"`
	Timestamp time.Time        `json:"timestamp"`
	Checks    map[string]Check `json:"checks"`
	Metrics   map[string]int64 `json:"metrics,omitempty"`
}

// Checker is a function that performs a health check
type Checker func(ctx context.Context) (Status, string)

// Server provides health check HTTP endpoints
type Server struct {
	port      string
	version   string
	startTime time.Time
	server    *http.Server

	mu       sync.RWMutex
	checkers map[string]Checker
	checks   map[string]Check
	metrics  map[string]*int64

	// Readiness flag - can be toggled during deployment
	ready int32
}

// NewServer creates a new health check server
func NewServer(port, version string) *Server {
	s := &Server{
		port:      port,
		version:   version,
		startTime: time.Now(),
		checkers:  make(map[string]Checker),
		checks:    make(map[string]Check),
		metrics:   make(map[string]*int64),
		ready:     1,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/health/live", s.livenessHandler)
	mux.HandleFunc("/health/ready", s.readinessHandler)
	mux.HandleFunc("/metrics", s.metricsHandler)

	s.server = &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	return s
}

// RegisterChecker registers a health checker
func (s *Server) RegisterChecker(name string, checker Checker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkers[name] = checker
}

// RegisterMetric registers a metric counter
func (s *Server) RegisterMetric(name string) *int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	counter := new(int64)
	s.metrics[name] = counter
	return counter
}

// SetReady sets the readiness state
func (s *Server) SetReady(ready bool) {
	if ready {
		atomic.StoreInt32(&s.ready, 1)
	} else {
		atomic.StoreInt32(&s.ready, 0)
	}
}

// IsReady returns the readiness state
func (s *Server) IsReady() bool {
	return atomic.LoadInt32(&s.ready) == 1
}

// Start starts the health check server
func (s *Server) Start() error {
	return s.server.ListenAndServe()
}

// Stop gracefully stops the server
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// RunChecks runs all registered health checks
func (s *Server) RunChecks(ctx context.Context) map[string]Check {
	s.mu.RLock()
	checkers := make(map[string]Checker)
	for k, v := range s.checkers {
		checkers[k] = v
	}
	s.mu.RUnlock()

	results := make(map[string]Check)
	var wg sync.WaitGroup

	for name, checker := range checkers {
		wg.Add(1)
		go func(n string, c Checker) {
			defer wg.Done()
			start := time.Now()
			status, msg := c(ctx)
			check := Check{
				Name:      n,
				Status:    status,
				Message:   msg,
				Duration:  time.Since(start),
				LastCheck: time.Now(),
			}
			s.mu.Lock()
			results[n] = check
			s.checks[n] = check
			s.mu.Unlock()
		}(name, checker)
	}

	wg.Wait()
	return results
}

// healthHandler handles /health requests
func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	checks := s.RunChecks(ctx)

	// Determine overall status
	overallStatus := StatusHealthy
	for _, check := range checks {
		if check.Status == StatusUnhealthy {
			overallStatus = StatusUnhealthy
			break
		}
		if check.Status == StatusDegraded && overallStatus == StatusHealthy {
			overallStatus = StatusDegraded
		}
	}

	// Collect metrics
	s.mu.RLock()
	metrics := make(map[string]int64)
	for name, counter := range s.metrics {
		metrics[name] = atomic.LoadInt64(counter)
	}
	s.mu.RUnlock()

	response := Response{
		Status:    overallStatus,
		Version:   s.version,
		Uptime:    time.Since(s.startTime).Round(time.Second).String(),
		Timestamp: time.Now().UTC(),
		Checks:    checks,
		Metrics:   metrics,
	}

	w.Header().Set("Content-Type", "application/json")
	if overallStatus == StatusUnhealthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(response)
}

// livenessHandler handles /health/live requests (k8s liveness probe)
func (s *Server) livenessHandler(w http.ResponseWriter, r *http.Request) {
	// Simple liveness - if we can respond, we're alive
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "alive",
	})
}

// readinessHandler handles /health/ready requests (k8s readiness probe)
func (s *Server) readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !s.IsReady() {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "not_ready",
		})
		return
	}

	// Run quick checks
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	checks := s.RunChecks(ctx)
	for _, check := range checks {
		if check.Status == StatusUnhealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "not_ready",
				"reason": check.Name + ": " + check.Message,
			})
			return
		}
	}

	json.NewEncoder(w).Encode(map[string]string{
		"status": "ready",
	})
}

// metricsHandler handles /metrics requests (Prometheus-compatible)
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain")
	for name, counter := range s.metrics {
		value := atomic.LoadInt64(counter)
		w.Write([]byte(name + " " + string(rune(value)) + "\n"))
	}
}

// Common health checkers

// SQSChecker creates a health checker for SQS
func SQSChecker(checkFn func(context.Context) error) Checker {
	return func(ctx context.Context) (Status, string) {
		if err := checkFn(ctx); err != nil {
			return StatusUnhealthy, "SQS unavailable: " + err.Error()
		}
		return StatusHealthy, "SQS connected"
	}
}

// DynamoDBChecker creates a health checker for DynamoDB
func DynamoDBChecker(checkFn func(context.Context) error) Checker {
	return func(ctx context.Context) (Status, string) {
		if err := checkFn(ctx); err != nil {
			return StatusUnhealthy, "DynamoDB unavailable: " + err.Error()
		}
		return StatusHealthy, "DynamoDB connected"
	}
}

// S3Checker creates a health checker for S3
func S3Checker(checkFn func(context.Context) error) Checker {
	return func(ctx context.Context) (Status, string) {
		if err := checkFn(ctx); err != nil {
			return StatusDegraded, "S3 unavailable: " + err.Error()
		}
		return StatusHealthy, "S3 connected"
	}
}
