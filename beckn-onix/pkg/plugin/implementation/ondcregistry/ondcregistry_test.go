package ondcregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

var fixedNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func sampleEntries() []Entry {
	from := fixedNow.Add(-24 * time.Hour)
	until := fixedNow.Add(24 * time.Hour)
	return []Entry{
		{SubscriberID: "buyer.example", UkID: "k1", Type: TypeBAP, Domain: "ONDC:RET10", City: "*", SigningPublicKey: "pkBAP", Status: "SUBSCRIBED", ValidFrom: from, ValidUntil: until},
		{SubscriberID: "gateway.example", UkID: "k2", Type: TypeBG, Domain: "ONDC:RET10", City: "*", SigningPublicKey: "pkBG", Status: "SUBSCRIBED", ValidFrom: from, ValidUntil: until},
		{SubscriberID: "pending.example", UkID: "k3", Type: TypeBAP, Domain: "ONDC:RET10", City: "*", Status: "INITIATED", ValidFrom: from, ValidUntil: until},
		{SubscriberID: "old.example", UkID: "k4", Type: TypeBAP, Domain: "ONDC:RET10", City: "*", Status: "SUBSCRIBED", ValidFrom: from.Add(-72 * time.Hour), ValidUntil: from},
		{SubscriberID: "future.example", UkID: "k5", Type: TypeBAP, Domain: "ONDC:RET10", City: "*", Status: "SUBSCRIBED", ValidFrom: until, ValidUntil: until.Add(24 * time.Hour)},
		{SubscriberID: "blr.example", UkID: "k6", Type: TypeBAP, Domain: "ONDC:RET10", City: "std:080", Status: "SUBSCRIBED", ValidFrom: from, ValidUntil: until},
	}
}

func newStubClient(t *testing.T) *Client {
	t.Helper()
	raw, _ := json.Marshal(sampleEntries())
	file := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(context.Background(), Config{Mode: "stub", File: file})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return fixedNow }
	return c
}

func TestFind(t *testing.T) {
	c := newStubClient(t)
	tests := []struct {
		name string
		q    Query
		want int
	}{
		{"subscribed bap", Query{SubscriberID: "buyer.example", UkID: "k1"}, 1},
		{"not subscribed", Query{SubscriberID: "pending.example"}, 0},
		{"expired window", Query{SubscriberID: "old.example"}, 0},
		{"not yet valid", Query{SubscriberID: "future.example"}, 0},
		{"unknown subscriber", Query{SubscriberID: "nobody"}, 0},
		{"wrong ukId", Query{SubscriberID: "buyer.example", UkID: "zzz"}, 0},
		{"type BG", Query{SubscriberID: "gateway.example", Type: TypeBG}, 1},
		{"wrong type", Query{SubscriberID: "gateway.example", Type: TypeBAP}, 0},
		{"domain matches ignoring case", Query{SubscriberID: "buyer.example", Domain: "ondc:ret10"}, 1},
		{"wrong domain", Query{SubscriberID: "buyer.example", Domain: "ONDC:RET11"}, 0},
		{"wildcard city matches any", Query{SubscriberID: "buyer.example", City: "std:011"}, 1},
		{"specific city matches", Query{SubscriberID: "blr.example", City: "std:080"}, 1},
		{"specific city mismatch", Query{SubscriberID: "blr.example", City: "std:011"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Find(context.Background(), tt.q)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.want {
				t.Errorf("Find() returned %d entries, want %d", len(got), tt.want)
			}
		})
	}
}

func TestLookupMapsToSubscription(t *testing.T) {
	c := newStubClient(t)
	subs, err := c.Lookup(context.Background(), &model.Subscription{
		Subscriber: model.Subscriber{SubscriberID: "buyer.example"},
		KeyID:      "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 {
		t.Fatalf("got %d subscriptions, want 1", len(subs))
	}
	s := subs[0]
	if s.KeyID != "k1" || s.SigningPublicKey != "pkBAP" || s.Status != "SUBSCRIBED" || s.Type != TypeBAP {
		t.Errorf("unexpected mapping: %+v", s)
	}
	if _, err := c.Lookup(context.Background(), nil); err == nil {
		t.Error("nil request must fail")
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"stub without file", Config{Mode: "stub"}},
		{"stub with missing file", Config{Mode: "stub", File: "/nonexistent/registry.json"}},
		{"http without url", Config{Mode: "http"}},
		{"unknown mode", Config{Mode: "carrier-pigeon"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(context.Background(), tt.cfg); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestHTTPMode(t *testing.T) {
	var gotPath string
	var gotQuery Query
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotQuery)
		// The server returns an entry that is not SUBSCRIBED: the client must still filter it out.
		_ = json.NewEncoder(w).Encode(sampleEntries())
	}))
	defer srv.Close()

	c, err := New(context.Background(), Config{Mode: "http", URL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return fixedNow }
	got, err := c.Find(context.Background(), Query{SubscriberID: "pending.example"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v2.0/lookup" {
		t.Errorf("path = %q, want /v2.0/lookup", gotPath)
	}
	if gotQuery.SubscriberID != "pending.example" {
		t.Errorf("query not sent: %+v", gotQuery)
	}
	if len(got) != 0 {
		t.Errorf("non-SUBSCRIBED entry must be filtered, got %d", len(got))
	}
}

func TestHTTPModeServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c, err := New(context.Background(), Config{Mode: "http", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Find(context.Background(), Query{SubscriberID: "x"}); err == nil {
		t.Error("a 502 from the registry must surface as an error")
	}
}
