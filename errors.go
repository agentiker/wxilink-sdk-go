package ilink

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// APIError represents an error response from the iLink API.
type APIError struct {
	Ret     int    `json:"ret"`
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ilink: api error ret=%d errcode=%d errmsg=%s", e.Ret, e.ErrCode, e.ErrMsg)
}

// IsSessionExpired reports whether the error indicates an expired session (errcode -14).
func (e *APIError) IsSessionExpired() bool {
	return e.ErrCode == -14 || e.Ret == -14
}

// IsNoSession reports whether the server refused to send because the target
// user has no active session with the bot (ret -2 "prepare failed"). This
// typically means the user has never messaged the bot, or the session lapsed.
func (e *APIError) IsNoSession() bool {
	return e.Ret == -2
}

// checkAPIError inspects a 2xx response body for the iLink error envelope
// (non-zero ret or errcode) and returns it as *APIError. Empty or non-JSON
// bodies are treated as success and left to the caller.
func checkAPIError(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var e APIError
	if err := json.Unmarshal(body, &e); err != nil {
		return nil
	}
	if e.Ret != 0 || e.ErrCode != 0 {
		return &e
	}
	return nil
}

// HTTPError represents a non-2xx HTTP response from the server.
type HTTPError struct {
	StatusCode int
	Body       []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("ilink: http %d: %s", e.StatusCode, string(e.Body))
}

// Sentinel errors.
var (
	// ErrNoContextToken is retained for API compatibility.
	//
	// Deprecated: [Client.Push] no longer returns it; it sends without a
	// token instead. Check *APIError.IsNoSession for the server-side refusal.
	ErrNoContextToken = errors.New("ilink: no cached context token; user must send a message first")
)
