package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

func TestExtractProtocolVersion_ONDC(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantVer  string
		wantONDC bool
	}{
		{"core_version", `{"context":{"core_version":"1.2.0"}}`, "1.2.0", true},
		{"version", `{"context":{"version":"1.1.0"}}`, "1.1.0", false},
		{"v2", `{"context":{"version":"2.0.0"}}`, "2.0.0", false},
		{"none", `{"context":{}}`, "", false},
		{"bad json", `{`, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, ondc := extractProtocolVersion([]byte(tt.body))
			if v != tt.wantVer || ondc != tt.wantONDC {
				t.Errorf("got (%q,%v), want (%q,%v)", v, ondc, tt.wantVer, tt.wantONDC)
			}
		})
	}
}

func TestExtractMessageID_ONDC(t *testing.T) {
	if got := extractMessageID([]byte(`{"context":{"message_id":"m-1"}}`)); got != "m-1" {
		t.Errorf("snake_case message_id: got %q", got)
	}
	if got := extractMessageID([]byte(`{"context":{"messageId":"m-2"}}`)); got != "m-2" {
		t.Errorf("camelCase messageId: got %q", got)
	}
}

func ondcCtx() context.Context {
	ctx := context.WithValue(context.Background(), model.ContextKeyProtocolVersion, "1.2.0")
	return context.WithValue(ctx, model.ContextKeyONDC, true)
}

func TestSendNack_ONDCShape(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantType   string
	}{
		{"bad signature", model.NewSignValidationErr("AUT_SIGNATURE_INVALID", http.ErrNotSupported), http.StatusUnauthorized, "30016", "CORE-ERROR"},
		{"schema", model.NewBadReqErr("", http.ErrNotSupported), http.StatusBadRequest, "30000", "JSON-SCHEMA-ERROR"},
		{"policy stale", model.NewBadReqErr("30022", http.ErrNotSupported), http.StatusBadRequest, "30022", "CONTEXT-ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			sendNack(ondcCtx(), rr, tt.err)
			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rr.Code, tt.wantStatus)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			msg := body["message"].(map[string]interface{})
			if msg["ack"].(map[string]interface{})["status"] != "NACK" {
				t.Errorf("ack status is not NACK: %v", msg)
			}
			if _, nested := msg["error"]; nested {
				t.Error("ONDC error must be top-level, not inside message")
			}
			e, ok := body["error"].(map[string]interface{})
			if !ok {
				t.Fatalf("no top-level error in %s", rr.Body.String())
			}
			if e["code"] != tt.wantCode || e["type"] != tt.wantType {
				t.Errorf("error = %v, want code %s type %s", e, tt.wantCode, tt.wantType)
			}
		})
	}
}

// A request without core_version must keep the original ONIX shape.
func TestSendNack_NonONDCUnchanged(t *testing.T) {
	ctx := context.WithValue(context.Background(), model.ContextKeyProtocolVersion, "1.1.0")
	rr := httptest.NewRecorder()
	sendNack(ctx, rr, model.NewSignValidationErr("AUT_SIGNATURE_INVALID", http.ErrNotSupported))
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, top := body["error"]; top {
		t.Errorf("non-ONDC response must not have top-level error: %s", rr.Body.String())
	}
	msg := body["message"].(map[string]interface{})
	e := msg["error"].(map[string]interface{})
	if e["code"] != "AUT_SIGNATURE_INVALID" {
		t.Errorf("code = %v", e["code"])
	}
	if _, hasType := e["type"]; hasType {
		t.Error("non-ONDC error must not carry a type")
	}
}
