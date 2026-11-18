package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Error codes the Autter API answers with ({"error": {"code": …}}).
const (
	CodeUnauthorized       = "unauthorized"
	CodeInsufficientScope  = "insufficient_scope"
	CodeInsufficientRole   = "insufficient_role"
	CodeFeatureDisabled    = "feature_disabled"
	CodeRepositoryNotFound = "repository_not_found"
	CodeNotFound           = "not_found"
	CodeNameConflict       = "name_conflict"
	CodeInvalidArgument    = "invalid_argument"
	CodeLimitReached       = "limit_reached"
	CodeRateLimited        = "rate_limited"
	CodeInternalError      = "internal_error"
)

// APIError is an error answer from the Autter API. A body that is not the
// API's error envelope (a proxy's 502, say) has the code "http_<status>" and
// the start of the body as its message.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Detail is the error's optional "detail" field, undecoded.
	Detail json.RawMessage
	// Method and Path say which request failed.
	Method string
	Path   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, e.Code, e.Message)
}

// IDs returns detail.ids, which a name_conflict carries.
func (e *APIError) IDs() []string {
	var detail struct {
		IDs []string `json:"ids"`
	}
	if len(e.Detail) == 0 || json.Unmarshal(e.Detail, &detail) != nil {
		return nil
	}
	return detail.IDs
}

// Candidates returns detail.candidates, which an ambiguous repository_not_found carries.
func (e *APIError) Candidates() []string {
	var detail struct {
		Candidates []string `json:"candidates"`
	}
	if len(e.Detail) == 0 || json.Unmarshal(e.Detail, &detail) != nil {
		return nil
	}
	return detail.Candidates
}

// ErrorCode returns the API error code of err, or "" when err is not an *APIError.
func ErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// IsNotFound reports whether err is the API's not_found (the resource, not the repository).
func IsNotFound(err error) bool { return ErrorCode(err) == CodeNotFound }

// IsRepositoryNotFound reports whether err is the API's repository_not_found.
func IsRepositoryNotFound(err error) bool { return ErrorCode(err) == CodeRepositoryNotFound }

// decodeError turns a non-2xx answer into an *APIError.
func decodeError(method, path string, status int, body []byte) *APIError {
	var envelope struct {
		Error *struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Detail  json.RawMessage `json:"detail"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error != nil && envelope.Error.Code != "" {
		return &APIError{Status: status, Code: envelope.Error.Code, Message: envelope.Error.Message, Detail: envelope.Error.Detail, Method: method, Path: path}
	}
	excerpt := strings.TrimSpace(string(body))
	if len(excerpt) > 200 {
		excerpt = excerpt[:200] + "…"
	}
	if excerpt == "" {
		excerpt = "(empty body)"
	}
	return &APIError{Status: status, Code: fmt.Sprintf("http_%d", status), Message: excerpt, Method: method, Path: path}
}
