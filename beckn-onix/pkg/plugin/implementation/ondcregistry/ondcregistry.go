// Package ondcregistry looks up ONDC network participants.
//
// It answers the question "who is this subscriber and which public key may I use
// to verify their signature?" the way the ONDC Registry does: entries use the
// ONDC field names (ukId, subscriber_url, ...) and only SUBSCRIBED entries that
// are inside their valid_from..valid_until window are returned.
//
// Two modes are supported:
//
//	stub  reads entries from a local JSON file. Use this while no ONDC Registry
//	      is available.
//	http  calls POST {url}/v2.0/lookup on a real ONDC Registry.
//	      TODO: the real /v2.0/lookup needs a signed Authorization header; that is
//	      not done yet.
package ondcregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

// Participant types used by ONDC.
const (
	TypeBAP = "BAP"
	TypeBPP = "BPP"
	TypeBG  = "BG"
)

// statusSubscribed is the only registry status that allows a participant to transact.
const statusSubscribed = "SUBSCRIBED"

// Entry is one registry record, using the ONDC field names.
type Entry struct {
	SubscriberID     string    `json:"subscriber_id"`
	UkID             string    `json:"ukId"`
	SubscriberURL    string    `json:"subscriber_url"`
	Type             string    `json:"type"`
	Domain           string    `json:"domain"`
	City             string    `json:"city"`
	Country          string    `json:"country"`
	SigningPublicKey string    `json:"signing_public_key"`
	EncrPublicKey    string    `json:"encr_public_key"`
	ValidFrom        time.Time `json:"valid_from"`
	ValidUntil       time.Time `json:"valid_until"`
	Status           string    `json:"status"`
}

// Query selects registry entries. An empty field matches anything.
type Query struct {
	SubscriberID string `json:"subscriber_id,omitempty"`
	UkID         string `json:"ukId,omitempty"`
	Type         string `json:"type,omitempty"`
	Domain       string `json:"domain,omitempty"`
	City         string `json:"city,omitempty"`
}

// Config configures a Client.
type Config struct {
	Mode    string        // "stub" (default) or "http"
	File    string        // stub mode: path of the JSON file (an array of Entry)
	URL     string        // http mode: registry base URL
	Timeout time.Duration // http mode: request timeout (default 5s)
}

// Client looks up registry entries.
type Client struct {
	cfg     Config
	entries []Entry
	http    *http.Client
	now     func() time.Time
}

// New builds a Client. In stub mode it reads the entries file once.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Mode == "" {
		cfg.Mode = "stub"
	}
	c := &Client{cfg: cfg, now: time.Now}
	switch cfg.Mode {
	case "stub":
		if cfg.File == "" {
			return nil, fmt.Errorf("ondcregistry: stub mode needs a file")
		}
		raw, err := os.ReadFile(cfg.File)
		if err != nil {
			return nil, fmt.Errorf("ondcregistry: read stub file: %w", err)
		}
		if err := json.Unmarshal(raw, &c.entries); err != nil {
			return nil, fmt.Errorf("ondcregistry: parse stub file %s: %w", cfg.File, err)
		}
	case "http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("ondcregistry: http mode needs a url")
		}
		timeout := cfg.Timeout
		if timeout == 0 {
			timeout = 5 * time.Second
		}
		c.http = &http.Client{Timeout: timeout}
	default:
		return nil, fmt.Errorf("ondcregistry: unknown mode %q (use stub or http)", cfg.Mode)
	}
	return c, nil
}

// Find returns the usable entries that match q.
func (c *Client) Find(ctx context.Context, q Query) ([]Entry, error) {
	var candidates []Entry
	if c.cfg.Mode == "http" {
		var err error
		if candidates, err = c.remoteLookup(ctx, q); err != nil {
			return nil, err
		}
	} else {
		candidates = c.entries
	}
	now := c.now()
	var out []Entry
	for _, e := range candidates {
		if matches(e, q) && usable(e, now) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Lookup implements definition.RegistryLookup, so ONIX's key manager can use
// this client to fetch the signing key of the signer named in the Authorization
// header (req.SubscriberID, req.KeyID = ukId).
func (c *Client) Lookup(ctx context.Context, req *model.Subscription) ([]model.Subscription, error) {
	if req == nil {
		return nil, fmt.Errorf("ondcregistry: nil lookup request")
	}
	found, err := c.Find(ctx, Query{SubscriberID: req.SubscriberID, UkID: req.KeyID})
	if err != nil {
		return nil, err
	}
	subs := make([]model.Subscription, 0, len(found))
	for _, e := range found {
		subs = append(subs, model.Subscription{
			Subscriber: model.Subscriber{
				SubscriberID: e.SubscriberID,
				URL:          e.SubscriberURL,
				Type:         e.Type,
				Domain:       e.Domain,
			},
			KeyID:            e.UkID,
			SigningPublicKey: e.SigningPublicKey,
			EncrPublicKey:    e.EncrPublicKey,
			ValidFrom:        e.ValidFrom,
			ValidUntil:       e.ValidUntil,
			Status:           e.Status,
		})
	}
	return subs, nil
}

func (c *Client) remoteLookup(ctx context.Context, q Query) ([]Entry, error) {
	body, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(c.cfg.URL, "/") + "/v2.0/lookup"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ondcregistry: lookup request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ondcregistry: registry answered %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("ondcregistry: bad registry response: %w", err)
	}
	return entries, nil
}

// usable reports whether e may be used for signature checks at time now: it must
// be SUBSCRIBED and now must lie inside valid_from..valid_until. A zero bound
// means "no limit".
func usable(e Entry, now time.Time) bool {
	if e.Status != statusSubscribed {
		return false
	}
	if !e.ValidFrom.IsZero() && now.Before(e.ValidFrom) {
		return false
	}
	if !e.ValidUntil.IsZero() && !now.Before(e.ValidUntil) {
		return false
	}
	return true
}

// matches reports whether e satisfies q. A registry city of "*" matches any city.
func matches(e Entry, q Query) bool {
	if q.SubscriberID != "" && e.SubscriberID != q.SubscriberID {
		return false
	}
	if q.UkID != "" && e.UkID != q.UkID {
		return false
	}
	if q.Type != "" && !strings.EqualFold(e.Type, q.Type) {
		return false
	}
	if q.Domain != "" && !strings.EqualFold(e.Domain, q.Domain) {
		return false
	}
	if q.City != "" && e.City != "*" && e.City != q.City {
		return false
	}
	return true
}
