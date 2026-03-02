package errors

import (
	"fmt"
)

// Error codes for the trade-lab platform
const (
	// General errors (1xxx)
	ErrUnknown = 1000 + iota
	ErrInvalidConfig
	ErrInvalidInput
	ErrNotFound
	ErrAlreadyExists
	ErrPermissionDenied

	// Database errors (2xxx)
	ErrDatabase = 2000 + iota
	ErrDatabaseConnect
	ErrDatabaseQuery
	ErrDatabaseTx

	// Market data errors (3xxx)
	ErrMarketData = 3000 + iota
	ErrMarketDataNotAvailable
	ErrMarketDataInvalid
	ErrMarketDataTimeout

	// Strategy errors (4xxx)
	ErrStrategy = 4000 + iota
	ErrStrategyNotFound
	ErrStrategyInvalid
	ErrStrategyRuntime

	// Order errors (5xxx)
	ErrOrder = 5000 + iota
	ErrOrderRejected
	ErrOrderTimeout
	ErrOrderInvalid
	ErrOrderInsufficientFunds

	// Risk control errors (6xxx)
	ErrRisk = 6000 + iota
	ErrRiskLimitExceeded
	ErrRiskDailyLossLimit
	ErrRiskPositionLimit
	ErrRiskCircuitBreaker
)

// AppError represents a structured application error
type AppError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Cause   error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%d] %s: %s (%v)", e.Code, e.Message, e.Detail, e.Cause)
	}
	if e.Detail != "" {
		return fmt.Sprintf("[%d] %s: %s", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.Cause
}

// New creates a new AppError
func New(code int, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Wrap wraps an error with code and message
func Wrap(code int, message string, cause error) *AppError {
	return &AppError{Code: code, Message: message, Cause: cause}
}

// WithDetail adds detail to the error
func (e *AppError) WithDetail(detail string) *AppError {
	e.Detail = detail
	return e
}

// Is checks if error matches the code
func Is(err error, code int) bool {
	if err == nil {
		return false
	}
	if appErr, ok := err.(*AppError); ok {
		return appErr.Code == code
	}
	return false
}

// Code returns the error code, or 0 if not an AppError
func Code(err error) int {
	if err == nil {
		return 0
	}
	if appErr, ok := err.(*AppError); ok {
		return appErr.Code
	}
	return ErrUnknown
}
