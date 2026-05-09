package git

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

type WebhookOperation string

const (
	WebhookOperationList     WebhookOperation = "list"
	WebhookOperationRegister WebhookOperation = "register"
	WebhookOperationDelete   WebhookOperation = "delete"
)

type WebhookErrorClass string

const (
	WebhookErrorClassUnknown      WebhookErrorClass = "Unknown"
	WebhookErrorClassUnauthorized WebhookErrorClass = "Unauthorized"
	WebhookErrorClassNotFound     WebhookErrorClass = "NotFound"
	WebhookErrorClassConflict     WebhookErrorClass = "Conflict"
	WebhookErrorClassRateLimited  WebhookErrorClass = "RateLimited"
	WebhookErrorClassTransient    WebhookErrorClass = "Transient"
)

// WebhookOperationError carries forge-agnostic webhook API failure metadata.
type WebhookOperationError struct {
	Provider   string
	Operation  WebhookOperation
	StatusCode int
	Err        error
}

func (e *WebhookOperationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s webhook %s failed (HTTP %d): %v", e.Provider, e.Operation, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("%s webhook %s failed: %v", e.Provider, e.Operation, e.Err)
}

func (e *WebhookOperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapWebhookOperationError(provider string, operation WebhookOperation, statusCode int, err error) error {
	if err == nil {
		return nil
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
		err = fmt.Errorf("%w: %v", ErrAuthFailed, err)
	}
	return &WebhookOperationError{
		Provider:   provider,
		Operation:  operation,
		StatusCode: statusCode,
		Err:        err,
	}
}

func ClassifyWebhookError(err error) WebhookErrorClass {
	if err == nil {
		return WebhookErrorClassUnknown
	}
	var webhookErr *WebhookOperationError
	if errors.As(err, &webhookErr) {
		return classifyWebhookStatusCode(webhookErr.StatusCode, webhookErr.Err)
	}
	return classifyWebhookStatusCode(0, err)
}

func IsWebhookErrorClass(err error, class WebhookErrorClass) bool {
	return ClassifyWebhookError(err) == class
}

func classifyWebhookStatusCode(statusCode int, err error) WebhookErrorClass {
	switch {
	case errors.Is(err, ErrAuthFailed):
		return WebhookErrorClassUnauthorized
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return WebhookErrorClassTransient
	}

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return WebhookErrorClassUnauthorized
	case http.StatusNotFound:
		return WebhookErrorClassNotFound
	case http.StatusConflict, http.StatusUnprocessableEntity:
		return WebhookErrorClassConflict
	case http.StatusTooManyRequests:
		return WebhookErrorClassRateLimited
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return WebhookErrorClassTransient
	}
	if statusCode >= 500 {
		return WebhookErrorClassTransient
	}
	return WebhookErrorClassUnknown
}
