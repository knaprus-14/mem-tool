package mem

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AnswerTransientError identifies a retryable transport/provider failure.
// Authentication, malformed responses and arbitrary provider errors do not
// implement this contract. RetryAfter is a minimum delay, never a deadline.
type AnswerTransientError struct {
	StatusCode int
	RetryAfter time.Duration
	Cause      error
}

func (e *AnswerTransientError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("answer provider temporarily unavailable (HTTP %d)", e.StatusCode)
	}
	return "answer provider temporarily unavailable (transport failure)"
}

func (e *AnswerTransientError) Unwrap() error { return e.Cause }

func retryableAnswerStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func answerTransportFailure(err error) error {
	var network *net.OpError
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && errors.As(err, &network) {
		return &AnswerTransientError{Cause: err}
	}
	return err
}

func parseAnswerRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

func classicMindMapAIRetryDelay(err error, retries int) (time.Duration, bool) {
	var transient *AnswerTransientError
	if retries >= 2 || !errors.As(err, &transient) || transient == nil {
		return 0, false
	}
	// A long provider cooldown is left for explicit resume. Never shorten the
	// provider's requested delay or keep a worker waiting without a bound.
	if transient.RetryAfter > 30*time.Second {
		return 0, false
	}
	return max(time.Second<<retries, transient.RetryAfter), true
}
