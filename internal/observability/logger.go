package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
)

// LogLevel represents logging levels
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l LogLevel) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// LogEntry represents a structured log entry
type LogEntry struct {
	Timestamp   string                 `json:"timestamp"`
	Level       string                 `json:"level"`
	Message     string                 `json:"message"`
	Service     string                 `json:"service,omitempty"`
	RequestID   string                 `json:"request_id,omitempty"`
	UserID      int                    `json:"user_id,omitempty"`
	ConnectionID string               `json:"connection_id,omitempty"`
	Duration    float64                `json:"duration_ms,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Stack       string                 `json:"stack,omitempty"`
	Fields      map[string]interface{} `json:"fields,omitempty"`
}

// Logger provides structured logging with context
type Logger struct {
	service   string
	minLevel  LogLevel
	mu        sync.Mutex
	output    *json.Encoder
}

// contextKey for logger context
type contextKey string

const (
	loggerKey    contextKey = "logger"
	requestIDKey contextKey = "request_id"
	userIDKey    contextKey = "user_id"
	connIDKey    contextKey = "connection_id"
)

var (
	defaultLogger *Logger
	once          sync.Once
)

// NewLogger creates a new structured logger
func NewLogger(service string, minLevel LogLevel) *Logger {
	return &Logger{
		service:  service,
		minLevel: minLevel,
		output:   json.NewEncoder(os.Stdout),
	}
}

// GetLogger returns the default logger, creating it if necessary
func GetLogger() *Logger {
	once.Do(func() {
		level := LevelInfo
		if os.Getenv("LOG_LEVEL") == "debug" {
			level = LevelDebug
		}
		service := os.Getenv("SERVICE_NAME")
		if service == "" {
			service = "messanger"
		}
		defaultLogger = NewLogger(service, level)
	})
	return defaultLogger
}

// WithContext adds logger to context
func WithContext(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext retrieves logger from context
func FromContext(ctx context.Context) *Logger {
	if l, ok := ctx.Value(loggerKey).(*Logger); ok {
		return l
	}
	return GetLogger()
}

// WithRequestID adds request ID to context
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// WithUserID adds user ID to context
func WithUserID(ctx context.Context, userID int) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// WithConnectionID adds connection ID to context
func WithConnectionID(ctx context.Context, connID string) context.Context {
	return context.WithValue(ctx, connIDKey, connID)
}

func (l *Logger) log(ctx context.Context, level LogLevel, msg string, fields map[string]interface{}, err error) {
	if level < l.minLevel {
		return
	}

	entry := LogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     level.String(),
		Message:   msg,
		Service:   l.service,
		Fields:    fields,
	}

	// Extract context values
	if reqID, ok := ctx.Value(requestIDKey).(string); ok {
		entry.RequestID = reqID
	}
	if userID, ok := ctx.Value(userIDKey).(int); ok {
		entry.UserID = userID
	}
	if connID, ok := ctx.Value(connIDKey).(string); ok {
		entry.ConnectionID = connID
	}

	if err != nil {
		entry.Error = err.Error()
		if level == LevelError {
			// Capture stack trace for errors
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			entry.Stack = string(buf[:n])
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.output.Encode(entry)
}

// Debug logs a debug message
func (l *Logger) Debug(ctx context.Context, msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(ctx, LevelDebug, msg, f, nil)
}

// Info logs an info message
func (l *Logger) Info(ctx context.Context, msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(ctx, LevelInfo, msg, f, nil)
}

// Warn logs a warning message
func (l *Logger) Warn(ctx context.Context, msg string, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(ctx, LevelWarn, msg, f, nil)
}

// Error logs an error message
func (l *Logger) Error(ctx context.Context, msg string, err error, fields ...map[string]interface{}) {
	var f map[string]interface{}
	if len(fields) > 0 {
		f = fields[0]
	}
	l.log(ctx, LevelError, msg, f, err)
}

// WithDuration logs with duration tracking
func (l *Logger) WithDuration(ctx context.Context, operation string, start time.Time, err error) {
	duration := time.Since(start).Milliseconds()
	fields := map[string]interface{}{
		"operation":   operation,
		"duration_ms": duration,
	}

	if err != nil {
		l.Error(ctx, fmt.Sprintf("%s failed", operation), err, fields)
	} else {
		l.Info(ctx, fmt.Sprintf("%s completed", operation), fields)
	}
}

// Timer returns a function to log duration
func (l *Logger) Timer(ctx context.Context, operation string) func(error) {
	start := time.Now()
	return func(err error) {
		l.WithDuration(ctx, operation, start, err)
	}
}
