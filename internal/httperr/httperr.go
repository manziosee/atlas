// Package httperr writes the control API's JSON error body. Every error has a
// human-readable message, a stable machine-readable code, and the request ID
// that appears in access logs.
package httperr

import (
	"encoding/json"
	"net/http"
)

// Stable error codes. Clients should branch on these rather than on messages.
const (
	CodeInvalidJSON        = "invalid_json"
	CodeValidation         = "validation_failed"
	CodeUnauthorized       = "unauthorized"
	CodeInsufficientScope  = "insufficient_scope"
	CodeNotFound           = "not_found"
	CodeRouteNotFound      = "route_not_found"
	CodeMethodNotAllowed   = "method_not_allowed"
	CodeNotOwner           = "not_owner"
	CodeOwnerAlive         = "owner_alive"
	CodeMemberAlive        = "member_alive"
	CodePreconditionFailed = "precondition_failed"
	CodeNoTTL              = "no_ttl"
	CodeRateLimited        = "rate_limited"
	CodePayloadTooLarge    = "payload_too_large"
	CodeNoHealthyInstance  = "no_healthy_instance"
	CodeUnavailable        = "unavailable"
	CodeInternal           = "internal"
)

// Body is the JSON error document.
type Body struct {
	Error     string `json:"error"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
	OwnerNode string `json:"owner_node,omitempty"`
}

// Write sends a JSON error. The request ID is read from the X-Request-Id
// response header set by the API's logging middleware.
func Write(w http.ResponseWriter, status int, code, message string) {
	WriteBody(w, status, Body{Error: message, Code: code})
}

// WriteBody sends a JSON error with optional fields such as OwnerNode.
func WriteBody(w http.ResponseWriter, status int, body Body) {
	if body.RequestID == "" {
		body.RequestID = w.Header().Get("X-Request-Id")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
