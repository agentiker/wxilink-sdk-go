package ilink

import (
	"errors"
	"testing"
)

func TestAPIError_Error(t *testing.T) {
	e := &APIError{Ret: -1, ErrCode: -14, ErrMsg: "session expired"}
	s := e.Error()
	if s == "" {
		t.Fatal("empty error string")
	}
}

func TestAPIError_IsSessionExpired(t *testing.T) {
	tests := []struct {
		name    string
		err     *APIError
		expired bool
	}{
		{"errcode -14", &APIError{ErrCode: -14}, true},
		{"ret -14", &APIError{Ret: -14}, true},
		{"both -14", &APIError{Ret: -14, ErrCode: -14}, true},
		{"not expired", &APIError{Ret: -1, ErrCode: -1}, false},
		{"zero", &APIError{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.IsSessionExpired(); got != tt.expired {
				t.Errorf("IsSessionExpired() = %v, want %v", got, tt.expired)
			}
		})
	}
}

func TestHTTPError_Error(t *testing.T) {
	e := &HTTPError{StatusCode: 500, Body: []byte("internal error")}
	s := e.Error()
	if s == "" {
		t.Fatal("empty error string")
	}
}

func TestErrNoContextToken(t *testing.T) {
	if !errors.Is(ErrNoContextToken, ErrNoContextToken) {
		t.Error("sentinel error should match itself")
	}
}

func TestAPIError_Classifiers(t *testing.T) {
	tests := []struct {
		err                         *APIError
		noSession, invalid, expired bool
	}{
		{&APIError{Ret: -2, ErrMsg: "prepare failed"}, true, false, false},
		{&APIError{Ret: -3, ErrMsg: "invalid arguments"}, false, true, false},
		{&APIError{ErrCode: -14, ErrMsg: "session timeout"}, false, false, true},
		{&APIError{Ret: -4}, false, false, false},
	}
	for _, tt := range tests {
		if got := tt.err.IsNoSession(); got != tt.noSession {
			t.Errorf("%v IsNoSession = %v", tt.err, got)
		}
		if got := tt.err.IsInvalidArgument(); got != tt.invalid {
			t.Errorf("%v IsInvalidArgument = %v", tt.err, got)
		}
		if got := tt.err.IsSessionExpired(); got != tt.expired {
			t.Errorf("%v IsSessionExpired = %v", tt.err, got)
		}
	}
}
