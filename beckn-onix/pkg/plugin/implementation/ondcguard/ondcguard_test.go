package ondcguard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcregistry"
)

var fixedNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// fakeRegistry knows "buyer" as a BAP, "gateway" as a BG and "seller" as a BPP.
type fakeRegistry struct{ fail bool }

func (f fakeRegistry) Find(_ context.Context, q ondcregistry.Query) ([]ondcregistry.Entry, error) {
	if f.fail {
		return nil, errors.New("registry down")
	}
	known := map[string]string{"buyer": ondcregistry.TypeBAP, "gateway": ondcregistry.TypeBG, "seller": ondcregistry.TypeBPP}
	if t, ok := known[q.SubscriberID]; ok && (q.Type == "" || q.Type == t) {
		return []ondcregistry.Entry{{SubscriberID: q.SubscriberID, Type: t}}, nil
	}
	return nil, nil
}

func newTestGuard(reg registry) *Guard {
	g := newGuard(Config{}, reg)
	g.now = func() time.Time { return fixedNow }
	return g
}

func authHeader(sub string) string {
	return fmt.Sprintf(`Signature keyId="%s|key-1|ed25519",algorithm="ed25519",created="1",expires="2",headers="(created) (expires) digest",signature="abc"`, sub)
}

func body(action, messageID string, ts time.Time, ttl string) []byte {
	return []byte(fmt.Sprintf(`{"context":{"action":%q,"domain":"ONDC:RET10","city":"std:080","bap_id":"buyer","transaction_id":"t-1","message_id":%q,"timestamp":%q,"ttl":%q}}`,
		action, messageID, ts.Format(time.RFC3339Nano), ttl))
}

func stepCtx(b []byte, auth, gateway string) *model.StepContext {
	r := httptest.NewRequest(http.MethodPost, "/bpp/receiver/search", nil)
	if auth != "" {
		r.Header.Set(model.AuthHeaderSubscriber, auth)
	}
	if gateway != "" {
		r.Header.Set(model.AuthHeaderGateway, gateway)
	}
	return &model.StepContext{Context: context.Background(), Request: r, Body: b, RespHeader: http.Header{}, SubID: "seller"}
}

// codeOf returns the ONIX error code and HTTP status carried by err.
func codeOf(t *testing.T, err error) (string, int) {
	t.Helper()
	var ce *model.CodedErr
	if !errors.As(err, &ce) {
		t.Fatalf("expected *model.CodedErr, got %T: %v", err, err)
	}
	return ce.BecknError().Code, ce.HTTPStatus()
}

func TestRun(t *testing.T) {
	fresh := fixedNow.Add(-5 * time.Second)
	tests := []struct {
		name       string
		ctx        *model.StepContext
		wantCode   string // "" means success
		wantStatus int
		wantMsg    string
	}{
		{"valid search", stepCtx(body("search", "m-ok", fresh, "PT30S"), authHeader("buyer"), authHeader("gateway")), "", 0, ""},
		{"valid select needs no gateway", stepCtx(body("select", "m-sel", fresh, "PT30S"), authHeader("buyer"), ""), "", 0, ""},
		{"search without gateway header", stepCtx(body("search", "m-nogw", fresh, "PT30S"), authHeader("buyer"), ""), "AUT_SIGNATURE_MISSING", 401, "X-Gateway-Authorization is required"},
		{"gateway signer is not a BG", stepCtx(body("search", "m-badgw", fresh, "PT30S"), authHeader("buyer"), authHeader("seller")), "AUT_UNAUTHORIZED_ACTION", 401, "not registered as BG"},
		{"sender is not a BAP", stepCtx(body("search", "m-badbap", fresh, "PT30S"), authHeader("seller"), authHeader("gateway")), "AUT_UNAUTHORIZED_ACTION", 401, "not registered as BAP"},
		{"unknown sender", stepCtx(body("search", "m-unk", fresh, "PT30S"), authHeader("stranger"), authHeader("gateway")), "AUT_UNAUTHORIZED_ACTION", 401, ""},
		{"unreadable keyId", stepCtx(body("search", "m-key", fresh, "PT30S"), "Signature nonsense", authHeader("gateway")), "AUT_SIGNATURE_INVALID", 401, ""},
		{"stale request", stepCtx(body("search", "m-stale", fixedNow.Add(-2*time.Minute), "PT30S"), authHeader("buyer"), authHeader("gateway")), "30022", 400, "stale request"},
		{"timestamp far in future", stepCtx(body("search", "m-fut", fixedNow.Add(10*time.Minute), "PT30S"), authHeader("buyer"), authHeader("gateway")), "30022", 400, "in the future"},
		{"bad ttl", stepCtx(body("search", "m-ttl", fresh, "30 seconds"), authHeader("buyer"), authHeader("gateway")), "30000", 400, "context.ttl"},
		{"bad timestamp", stepCtx([]byte(`{"context":{"action":"search","timestamp":"yesterday","ttl":"PT30S"}}`), authHeader("buyer"), authHeader("gateway")), "30000", 400, "RFC 3339"},
		{"body not json", stepCtx([]byte(`nope`), authHeader("buyer"), authHeader("gateway")), "30000", 400, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := newTestGuard(fakeRegistry{}).Run(tt.ctx)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected an error")
			}
			code, status := codeOf(t, err)
			if code != tt.wantCode || status != tt.wantStatus {
				t.Errorf("got code %s status %d, want %s %d (%v)", code, status, tt.wantCode, tt.wantStatus, err)
			}
			if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestMissingGatewaySetsChallengeHeader(t *testing.T) {
	sc := stepCtx(body("search", "m-1", fixedNow, "PT30S"), authHeader("buyer"), "")
	if err := newTestGuard(fakeRegistry{}).Run(sc); err == nil {
		t.Fatal("expected an error")
	}
	if sc.RespHeader.Get(model.UnaAuthorizedHeaderGateway) == "" {
		t.Error("Proxy-Authenticate must be set when the gateway header is missing")
	}
}

func TestReplay(t *testing.T) {
	g := newTestGuard(fakeRegistry{})
	run := func(messageID string) error {
		return g.Run(stepCtx(body("select", messageID, fixedNow, "PT30S"), authHeader("buyer"), ""))
	}
	if err := run("m-1"); err != nil {
		t.Fatalf("first request: %v", err)
	}
	err := run("m-1")
	if err == nil {
		t.Fatal("second request with the same message_id must be rejected")
	}
	if code, _ := codeOf(t, err); code != "30000" || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("got %q: %v", code, err)
	}
	if err := run("m-2"); err != nil {
		t.Errorf("a different message_id must pass: %v", err)
	}

	// After the replay window has passed the same id is accepted again.
	g.now = func() time.Time { return fixedNow.Add(10 * time.Minute) }
	if err := g.Run(stepCtx(body("select", "m-1", fixedNow.Add(10*time.Minute), "PT30S"), authHeader("buyer"), "")); err != nil {
		t.Errorf("replay entry must expire: %v", err)
	}
}

// A request that fails an earlier check must not use up its message_id.
func TestRejectedRequestDoesNotBurnMessageID(t *testing.T) {
	g := newTestGuard(fakeRegistry{})
	if err := g.Run(stepCtx(body("search", "m-x", fixedNow, "PT30S"), authHeader("buyer"), "")); err == nil {
		t.Fatal("expected the missing gateway header to be rejected")
	}
	if err := g.Run(stepCtx(body("search", "m-x", fixedNow, "PT30S"), authHeader("buyer"), authHeader("gateway"))); err != nil {
		t.Errorf("the corrected request must pass: %v", err)
	}
}

func TestRegistryFailureIsNotAClientError(t *testing.T) {
	err := newTestGuard(fakeRegistry{fail: true}).Run(stepCtx(body("select", "m-1", fixedNow, "PT30S"), authHeader("buyer"), ""))
	if err == nil {
		t.Fatal("expected an error")
	}
	var ce *model.CodedErr
	if errors.As(err, &ce) {
		t.Errorf("a registry outage must not look like a 4xx coded error: %v", err)
	}
}

func TestParseISODuration(t *testing.T) {
	good := map[string]time.Duration{
		"PT30S":   30 * time.Second,
		"PT5M":    5 * time.Minute,
		"PT1H30M": 90 * time.Minute,
		"P1D":     24 * time.Hour,
		"P1DT12H": 36 * time.Hour,
		"PT0.5S":  500 * time.Millisecond,
	}
	for in, want := range good {
		got, err := ParseISODuration(in)
		if err != nil || got != want {
			t.Errorf("ParseISODuration(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "P", "PT", "30s", "PT-5S", "P1Y", "1H", "PTS"} {
		if _, err := ParseISODuration(in); err == nil {
			t.Errorf("ParseISODuration(%q) must fail", in)
		}
	}
}

func TestParseKeyID(t *testing.T) {
	sub, uk, ok := parseKeyID(authHeader("buyer"))
	if !ok || sub != "buyer" || uk != "key-1" {
		t.Errorf("got %q %q %v", sub, uk, ok)
	}
	for _, h := range []string{"", `Signature keyId="only-one"`, `Signature keyId="a|b"`, `Signature keyId="|k|ed25519"`, `Signature keyId="a|b|ed25519`} {
		if _, _, ok := parseKeyID(h); ok {
			t.Errorf("parseKeyID(%q) must fail", h)
		}
	}
}
