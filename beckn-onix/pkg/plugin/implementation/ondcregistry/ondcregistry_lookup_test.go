package ondcregistry

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/signvalidator"
)

// The request must use the field names of ONDC's own v2.0 lookup tool
// (reference-implementations/utilities/v2lookup utility): "ukid" in lower case,
// plus "country".
func TestLookupRequestUsesONDCFieldNames(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &raw)
		_, _ = io.WriteString(w, "[]")
	}))
	defer srv.Close()

	c, _ := New(context.Background(), Config{Mode: "http", URL: srv.URL})
	_, err := c.Find(context.Background(), Query{SubscriberID: "buyer.example", UkID: "k1", Type: TypeBAP, Domain: "ONDC:RET10", City: "std:080", Country: "IND"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"subscriber_id": "buyer.example", "ukid": "k1", "type": "BAP", "domain": "ONDC:RET10", "city": "std:080", "country": "IND"}
	for k, v := range want {
		if raw[k] != v {
			t.Errorf("request field %q = %v, want %q (request was %v)", k, raw[k], v, raw)
		}
	}
	if _, wrong := raw["ukId"]; wrong {
		t.Error(`request must use "ukid", not "ukId"`)
	}
}

// A real registry accepts a lookup only when it is signed. The header must be
// verifiable with our own validator (same method ONDC's sample code uses), over the
// exact body that was sent.
func TestLookupRequestIsSigned(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	for name, key := range map[string]string{
		"32-byte seed":          base64.StdEncoding.EncodeToString(priv.Seed()),
		"64-byte ONDC key form": base64.StdEncoding.EncodeToString(priv),
	} {
		t.Run(name, func(t *testing.T) {
			var gotHeader string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeader = r.Header.Get("Authorization")
				gotBody, _ = io.ReadAll(r.Body)
				_, _ = io.WriteString(w, "[]")
			}))
			defer srv.Close()

			c, err := New(context.Background(), Config{Mode: "http", URL: srv.URL,
				CallerID: "seller.example", CallerKeyID: "key-9", CallerPrivateKey: key})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Find(context.Background(), Query{SubscriberID: "buyer.example"}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(gotHeader, `keyId="seller.example|key-9|ed25519"`) {
				t.Fatalf("keyId missing in header: %q", gotHeader)
			}
			v, _, _ := signvalidator.New(context.Background(), &signvalidator.Config{})
			err = v.Validate(&model.StepContext{Context: context.Background(), Body: gotBody}, gotHeader, base64.StdEncoding.EncodeToString(pub), false)
			if err != nil {
				t.Errorf("the lookup signature does not verify: %v", err)
			}
			// and it must NOT verify if the body is changed
			err = v.Validate(&model.StepContext{Context: context.Background(), Body: append(gotBody, ' ')}, gotHeader, base64.StdEncoding.EncodeToString(pub), false)
			if err == nil {
				t.Error("a changed body must not verify")
			}
		})
	}
}

func TestLookupUnsignedWhenNoKeyConfigured(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "[]")
	}))
	defer srv.Close()
	c, _ := New(context.Background(), Config{Mode: "http", URL: srv.URL})
	_, _ = c.Find(context.Background(), Query{SubscriberID: "x"})
	if gotHeader != "" {
		t.Errorf("no key configured, yet an Authorization header was sent: %q", gotHeader)
	}
}

func TestNewRejectsIncompleteCallerIdentity(t *testing.T) {
	_, err := New(context.Background(), Config{Mode: "http", URL: "http://x", CallerPrivateKey: "AAAA"})
	if err == nil {
		t.Error("a private key without callerId and callerKeyId must be refused")
	}
}

func TestCountryFilter(t *testing.T) {
	c := newStubClient(t)
	for i := range c.entries {
		c.entries[i].Country = "IND"
	}
	if got, _ := c.Find(context.Background(), Query{SubscriberID: "buyer.example", Country: "IND"}); len(got) != 1 {
		t.Errorf("same country must match, got %d", len(got))
	}
	if got, _ := c.Find(context.Background(), Query{SubscriberID: "buyer.example", Country: "USA"}); len(got) != 0 {
		t.Errorf("other country must not match, got %d", len(got))
	}
}

// The response shape, with the field names of an ONDC registry entry (as quoted from
// the ONDC developer-docs sample in our research notes; we could not re-fetch the
// original page). The key id appears as "ukId"; Go's JSON also accepts "ukid".
func TestParsesONDCRegistryResponseShape(t *testing.T) {
	sample := `[{
	  "subscriber_id": "buyer-app.example.org",
	  "status": "SUBSCRIBED",
	  "ukId": "UKID1",
	  "subscriber_url": "/ondc",
	  "country": "IND",
	  "domain": "ONDC:RET10",
	  "valid_from": "2023-02-20T11:43:00.000Z",
	  "valid_until": "2099-02-20T11:43:00.000Z",
	  "type": "BAP",
	  "signing_public_key": "c2lnbmluZw==",
	  "encr_public_key": "ZW5jcg==",
	  "city": "std:080",
	  "created": "2023-02-20T11:43:00.000Z",
	  "updated": "2023-02-20T11:43:00.000Z",
	  "br_id": "buyer-app.example.org"
	}, {
	  "subscriber_id": "buyer-app.example.org", "status": "SUBSCRIBED", "ukid": "UKID2",
	  "type": "BAP", "domain": "ONDC:RET10", "city": "*", "country": "IND",
	  "valid_from": "2023-02-20T11:43:00Z", "valid_until": "2099-02-20T11:43:00Z"
	}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, sample) }))
	defer srv.Close()
	c, _ := New(context.Background(), Config{Mode: "http", URL: srv.URL})
	c.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

	got, err := c.Find(context.Background(), Query{SubscriberID: "buyer-app.example.org"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].UkID != "UKID1" || got[0].SubscriberURL != "/ondc" || got[0].SigningPublicKey != "c2lnbmluZw==" || got[0].City != "std:080" {
		t.Errorf("fields not read correctly: %+v", got[0])
	}
	if got[1].UkID != "UKID2" {
		t.Errorf(`lower-case "ukid" in a response must also be read, got %q`, got[1].UkID)
	}
}

// A real registry is reached over the network, so answers are cached for a short time.
func TestRemoteLookupIsCached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(sampleEntries())
	}))
	defer srv.Close()

	now := fixedNow
	c, _ := New(context.Background(), Config{Mode: "http", URL: srv.URL, CacheTTL: time.Minute})
	c.now = func() time.Time { return now }

	q := Query{SubscriberID: "buyer.example", UkID: "k1"}
	for i := 0; i < 5; i++ {
		if got, err := c.Find(context.Background(), q); err != nil || len(got) != 1 {
			t.Fatalf("Find: %v, %d entries", err, len(got))
		}
	}
	if hits != 1 {
		t.Errorf("5 identical lookups made %d registry calls, want 1", hits)
	}
	// a different question is a different call
	_, _ = c.Find(context.Background(), Query{SubscriberID: "gateway.example"})
	if hits != 2 {
		t.Errorf("a different query must call the registry, hits = %d", hits)
	}
	// after the time limit the registry is asked again
	now = now.Add(2 * time.Minute)
	_, _ = c.Find(context.Background(), q)
	if hits != 3 {
		t.Errorf("an expired cache entry must be refreshed, hits = %d", hits)
	}
}

func TestRemoteLookupCacheCanBeSwitchedOff(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = io.WriteString(w, "[]")
	}))
	defer srv.Close()
	c, _ := New(context.Background(), Config{Mode: "http", URL: srv.URL, CacheTTL: -1})
	for i := 0; i < 3; i++ {
		_, _ = c.Find(context.Background(), Query{SubscriberID: "x"})
	}
	if hits != 3 {
		t.Errorf("with the cache off, 3 lookups must make 3 calls, got %d", hits)
	}
}

// The cache keeps raw entries, so the validity window is still checked on every use.
func TestCachedEntryStillChecksValidityWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sampleEntries())
	}))
	defer srv.Close()
	now := fixedNow
	c, _ := New(context.Background(), Config{Mode: "http", URL: srv.URL, CacheTTL: 48 * time.Hour})
	c.now = func() time.Time { return now }
	q := Query{SubscriberID: "buyer.example"}
	if got, _ := c.Find(context.Background(), q); len(got) != 1 {
		t.Fatal("entry should be usable now")
	}
	now = now.Add(30 * time.Hour) // still cached, but valid_until (fixedNow + 24h) has passed
	if got, _ := c.Find(context.Background(), q); len(got) != 0 {
		t.Error("a cached entry must not be used after its valid_until")
	}
}

func TestMatchIsExported(t *testing.T) {
	e := Entry{SubscriberID: "a", UkID: "k", Type: TypeBAP, Domain: "ONDC:RET10", City: "*", Country: "IND"}
	if !Match(e, Query{SubscriberID: "a", City: "std:080"}) {
		t.Error("wildcard city must match")
	}
	if Match(e, Query{SubscriberID: "b"}) {
		t.Error("other subscriber must not match")
	}
}
