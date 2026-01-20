package resilience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// CircuitState represents the circuit breaker state
type CircuitState int

const (
	StateClosed CircuitState = iota
	StateOpen
	StateHalfOpen
)

func (s CircuitState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

var (
	ErrCircuitOpen    = errors.New("circuit breaker is open")
	ErrTooManyFailures = errors.New("too many failures")
)

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	name              string
	maxFailures       int
	resetTimeout      time.Duration
	halfOpenMaxCalls  int

	mu                sync.RWMutex
	state             CircuitState
	failures          int
	successes         int
	lastFailure       time.Time
	halfOpenCalls     int
}

// CircuitBreakerConfig configures a circuit breaker
type CircuitBreakerConfig struct {
	Name             string
	MaxFailures      int           // Failures before opening
	ResetTimeout     time.Duration // Time before trying half-open
	HalfOpenMaxCalls int           // Max calls in half-open state
}

// DefaultConfig returns default circuit breaker config
func DefaultConfig(name string) CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Name:             name,
		MaxFailures:      5,
		ResetTimeout:     30 * time.Second,
		HalfOpenMaxCalls: 3,
	}
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		name:             cfg.Name,
		maxFailures:      cfg.MaxFailures,
		resetTimeout:     cfg.ResetTimeout,
		halfOpenMaxCalls: cfg.HalfOpenMaxCalls,
		state:            StateClosed,
	}
}

// Execute runs a function with circuit breaker protection
func (cb *CircuitBreaker) Execute(ctx context.Context, fn func(context.Context) error) error {
	if err := cb.allowRequest(); err != nil {
		return err
	}

	err := fn(ctx)
	cb.recordResult(err)
	return err
}

// allowRequest checks if a request is allowed
func (cb *CircuitBreaker) allowRequest() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return nil

	case StateOpen:
		// Check if we should transition to half-open
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			cb.state = StateHalfOpen
			cb.halfOpenCalls = 0
			cb.successes = 0
			return nil
		}
		return ErrCircuitOpen

	case StateHalfOpen:
		if cb.halfOpenCalls >= cb.halfOpenMaxCalls {
			return ErrCircuitOpen
		}
		cb.halfOpenCalls++
		return nil
	}

	return nil
}

// recordResult records the result of an operation
func (cb *CircuitBreaker) recordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failures++
		cb.lastFailure = time.Now()

		switch cb.state {
		case StateClosed:
			if cb.failures >= cb.maxFailures {
				cb.state = StateOpen
			}
		case StateHalfOpen:
			// Any failure in half-open state opens the circuit
			cb.state = StateOpen
		}
	} else {
		cb.successes++

		switch cb.state {
		case StateClosed:
			// Reset failures on success
			cb.failures = 0
		case StateHalfOpen:
			// After enough successes, close the circuit
			if cb.successes >= cb.halfOpenMaxCalls {
				cb.state = StateClosed
				cb.failures = 0
				cb.successes = 0
			}
		}
	}
}

// State returns the current state
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Stats returns circuit breaker statistics
func (cb *CircuitBreaker) Stats() map[string]interface{} {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return map[string]interface{}{
		"name":        cb.name,
		"state":       cb.state.String(),
		"failures":    cb.failures,
		"successes":   cb.successes,
		"last_failure": cb.lastFailure,
	}
}

// Reset manually resets the circuit breaker
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failures = 0
	cb.successes = 0
	cb.halfOpenCalls = 0
}

// CircuitBreakerRegistry manages multiple circuit breakers
type CircuitBreakerRegistry struct {
	breakers sync.Map
}

// NewRegistry creates a new circuit breaker registry
func NewRegistry() *CircuitBreakerRegistry {
	return &CircuitBreakerRegistry{}
}

// Get returns or creates a circuit breaker by name
func (r *CircuitBreakerRegistry) Get(name string) *CircuitBreaker {
	if cb, ok := r.breakers.Load(name); ok {
		return cb.(*CircuitBreaker)
	}

	cb := NewCircuitBreaker(DefaultConfig(name))
	actual, _ := r.breakers.LoadOrStore(name, cb)
	return actual.(*CircuitBreaker)
}

// GetWithConfig returns or creates a circuit breaker with config
func (r *CircuitBreakerRegistry) GetWithConfig(cfg CircuitBreakerConfig) *CircuitBreaker {
	if cb, ok := r.breakers.Load(cfg.Name); ok {
		return cb.(*CircuitBreaker)
	}

	cb := NewCircuitBreaker(cfg)
	actual, _ := r.breakers.LoadOrStore(cfg.Name, cb)
	return actual.(*CircuitBreaker)
}

// AllStats returns stats for all circuit breakers
func (r *CircuitBreakerRegistry) AllStats() map[string]interface{} {
	stats := make(map[string]interface{})
	r.breakers.Range(func(key, value interface{}) bool {
		stats[key.(string)] = value.(*CircuitBreaker).Stats()
		return true
	})
	return stats
}

// Global registry
var globalRegistry = NewRegistry()

// GetCircuitBreaker returns a circuit breaker from the global registry
func GetCircuitBreaker(name string) *CircuitBreaker {
	return globalRegistry.Get(name)
}

// GetRegistry returns the global registry
func GetRegistry() *CircuitBreakerRegistry {
	return globalRegistry
}
