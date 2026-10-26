package reqpreprocessor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

func serve(t *testing.T, cfg *Config, body string) (*httptest.ResponseRecorder, bool, bool) {
	t.Helper()
	var reached, ondc bool
	mw, err := NewPreProcessor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached, ondc = true, model.IsONDC(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/bpp/receiver/search", strings.NewReader(body)))
	return rec, reached, ondc
}

// With ondc enabled, a body that is not JSON is refused in the ONDC shape,
// because we can no longer find core_version in it.
func TestONDCModeRefusesBadJsonInONDCShape(t *testing.T) {
	rec, reached, _ := serve(t, &Config{Role: "bpp", ONDC: true}, "this is not json")
	if reached || rec.Code != http.StatusBadRequest {
		t.Fatalf("reached=%v status=%d; want refused with 400", reached, rec.Code)
	}
	var doc struct {
		Message struct {
			Ack struct{ Status string } `json:"ack"`
		} `json:"message"`
		Error struct{ Type, Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %s", rec.Body.String())
	}
	if doc.Message.Ack.Status != "NACK" || doc.Error.Code != "30000" || doc.Error.Type != "JSON-SCHEMA-ERROR" {
		t.Errorf("want NACK 30000 JSON-SCHEMA-ERROR, got %s", rec.Body.String())
	}
}

// Without the setting the original behaviour is unchanged.
func TestWithoutONDCModeBadJsonKeepsOriginalBody(t *testing.T) {
	rec, _, _ := serve(t, &Config{Role: "bpp"}, "this is not json")
	var doc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &doc)
	if doc["code"] != "SCH_INVALID_JSON" {
		t.Errorf("original body must be unchanged, got %s", rec.Body.String())
	}
	if _, has := doc["error"]; has {
		t.Error("must not be the ONDC shape")
	}
}

// With ondc enabled every request that passes through is marked as ONDC, even
// when its body has no core_version, so later steps answer in the ONDC shape.
func TestONDCModeMarksEveryRequest(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":        "",
		"no core_version":   `{"context":{"bap_id":"b","action":"search"}}`,
		"with core_version": `{"context":{"bap_id":"b","core_version":"1.2.0"}}`,
	} {
		_, reached, ondc := serve(t, &Config{Role: "bpp", ONDC: true}, body)
		if !reached || !ondc {
			t.Errorf("%s: reached=%v ondc=%v, want both true", name, reached, ondc)
		}
	}
	_, reached, ondc := serve(t, &Config{Role: "bpp"}, `{"context":{"bap_id":"b"}}`)
	if !reached || ondc {
		t.Errorf("without the setting: reached=%v ondc=%v, want true and false", reached, ondc)
	}
}
