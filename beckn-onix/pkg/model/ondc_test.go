package model

import (
	"context"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name     string
		ctx      map[string]interface{}
		wantVer  string
		wantONDC bool
	}{
		{"beckn version wins", map[string]interface{}{"version": "2.0.0", "core_version": "1.2.0"}, "2.0.0", false},
		{"ondc core_version", map[string]interface{}{"core_version": "1.2.0"}, "1.2.0", true},
		{"only version", map[string]interface{}{"version": "1.1.0"}, "1.1.0", false},
		{"empty version falls back", map[string]interface{}{"version": "", "core_version": "1.2.0"}, "1.2.0", true},
		{"neither", map[string]interface{}{}, "", false},
		{"wrong type is ignored", map[string]interface{}{"core_version": 12}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ondc := ResolveVersion(tt.ctx)
			if v != tt.wantVer || ondc != tt.wantONDC {
				t.Errorf("ResolveVersion() = (%q, %v), want (%q, %v)", v, ondc, tt.wantVer, tt.wantONDC)
			}
		})
	}
}

func TestIsONDC(t *testing.T) {
	if IsONDC(context.Background()) {
		t.Error("IsONDC on a plain context must be false")
	}
	if !IsONDC(context.WithValue(context.Background(), ContextKeyONDC, true)) {
		t.Error("IsONDC must be true when the marker is set")
	}
}

func TestToONDCError(t *testing.T) {
	tests := []struct {
		name     string
		in       *Error
		wantCode string
		wantType string
	}{
		{"signature invalid", &Error{Code: "AUT_SIGNATURE_INVALID", Message: "m"}, ONDCCodeInvalidSignature, ONDCTypeCore},
		{"signature missing", &Error{Code: "AUT_SIGNATURE_MISSING", Message: "m"}, ONDCCodeInvalidSignature, ONDCTypeCore},
		{"schema failure", &Error{Code: "SCH_SCHEMA_VALIDATION_FAILED", Message: "m"}, ONDCCodeInvalidRequest, ONDCTypeSchema},
		{"policy failure", &Error{Code: "POL_GENERIC_ERROR", Message: "m"}, ONDCCodeInvalidRequest, ONDCTypePolicy},
		{"stale request keeps code", &Error{Code: "30022", Message: "m"}, ONDCCodeStaleRequest, ONDCTypeContext},
		{"numeric invalid request", &Error{Code: "30000", Message: "m"}, ONDCCodeInvalidRequest, ONDCTypePolicy},
		{"unknown code", &Error{Code: "NET_INTERNAL_ERROR", Message: "m"}, ONDCCodeInternalError, ONDCTypeCore},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToONDCError(tt.in)
			if got.Code != tt.wantCode || got.Type != tt.wantType {
				t.Errorf("ToONDCError() = (%q, %q), want (%q, %q)", got.Code, got.Type, tt.wantCode, tt.wantType)
			}
			if got.Message != tt.in.Message {
				t.Errorf("message changed: %q", got.Message)
			}
			if tt.in.Type != "" {
				t.Error("input must not be modified")
			}
		})
	}
	if ToONDCError(nil) != nil {
		t.Error("ToONDCError(nil) must be nil")
	}
}
