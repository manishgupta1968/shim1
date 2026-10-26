package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcregistry"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/signvalidator"
)

const ackBody = `{"message":{"ack":{"status":"ACK"}}}`

// received is one request seen by a stub.
type received struct {
	Action    string `json:"action"`
	MessageID string `json:"message_id"`
	Verified  *bool  `json:"verified,omitempty"` // bap stub only: did the signature verify?
	Detail    string `json:"detail,omitempty"`
}

// inbox is a small thread-safe list shared by the stub handlers.
type inbox struct {
	mu    sync.Mutex
	items []received
}

func (b *inbox) add(r received) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, r)
}

func (b *inbox) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b.items)
	}
}

func readContext(body []byte) map[string]any {
	var doc struct {
		Context map[string]any `json:"context"`
	}
	_ = json.Unmarshal(body, &doc)
	return doc.Context
}

// runBackend is the stub for the seller's business logic. Shim 1 forwards a
// request here only after every pre-processing check passed.
func runBackend(args []string) error {
	fs := flag.NewFlagSet("backend", flag.ExitOnError)
	port := fs.Int("port", 9001, "listen port")
	onix := fs.String("onix", "http://localhost:8081", "Shim 1 base URL, used to send callbacks")
	fs.Parse(args)

	var seen inbox
	mux := http.NewServeMux()
	mux.HandleFunc("/ondc/received", seen.handler())
	mux.HandleFunc("/ondc/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		action := strings.TrimPrefix(r.URL.Path, "/ondc/")
		c := readContext(body)
		mid, _ := c["message_id"].(string)
		seen.add(received{Action: action, MessageID: mid})
		log.Printf("[seller backend] got %s message_id=%s  (all pre-processing checks passed)", action, mid)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ackBody)

		if action == "search" {
			go sendOnSearch(*onix, c)
		}
	})
	log.Printf("[seller backend] listening on :%d", *port)
	return http.ListenAndServe(fmt.Sprintf(":%d", *port), mux)
}

// sendOnSearch is the seller's "functional processing" of a search: it answers
// with a canned catalog. It posts the callback to Shim 1, which signs it and
// delivers it to the buyer app (bap_uri).
func sendOnSearch(onix string, searchCtx map[string]any) {
	time.Sleep(200 * time.Millisecond) // the ACK goes first, then the callback
	ctx := map[string]any{}
	for k, v := range searchCtx {
		ctx[k] = v
	}
	ctx["action"] = "on_search"
	ctx["bpp_id"] = sellerID
	ctx["bpp_uri"] = sellerURI
	ctx["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)

	payload, _ := json.Marshal(map[string]any{
		"context": ctx,
		"message": map[string]any{"catalog": map[string]any{
			"bpp/providers": []any{map[string]any{
				"id":         "provider-1",
				"descriptor": map[string]any{"name": "Demo Coffee Shop"},
				"items": []any{map[string]any{
					"id":         "item-1",
					"descriptor": map[string]any{"name": "Filter coffee"},
					"price":      map[string]any{"currency": "INR", "value": "120.00"},
				}},
			}},
		}},
	})
	resp, err := http.Post(onix+"/bpp/caller/on_search", "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[seller backend] on_search failed: %v", err)
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	log.Printf("[seller backend] sent on_search through Shim 1, answer: %d %s", resp.StatusCode, strings.TrimSpace(string(out)))
}

// runBAP is the stub buyer app. It receives the callbacks and checks, with the
// ONIX signature validator and the stub registry, that Shim 1 signed them properly.
func runBAP(args []string) error {
	fs := flag.NewFlagSet("bap", flag.ExitOnError)
	port := fs.Int("port", 9002, "listen port")
	registryFile := fs.String("registry", "config/shim1/generated/registry.json", "stub registry file")
	fs.Parse(args)

	reg, err := ondcregistry.New(context.Background(), ondcregistry.Config{Mode: "stub", File: *registryFile})
	if err != nil {
		return err
	}
	validator, _, err := signvalidator.New(context.Background(), &signvalidator.Config{})
	if err != nil {
		return err
	}

	var seen inbox
	mux := http.NewServeMux()
	mux.HandleFunc("/bap/received", seen.handler())
	mux.HandleFunc("/bap/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		action := strings.TrimPrefix(r.URL.Path, "/bap/")
		mid, _ := readContext(body)["message_id"].(string)

		header := r.Header.Get(model.AuthHeaderSubscriber)
		ok, detail := false, ""
		sub, ukID, parsed := splitKeyID(header)
		if !parsed {
			detail = "no usable Authorization header"
		} else if found, ferr := reg.Lookup(r.Context(), &model.Subscription{Subscriber: model.Subscriber{SubscriberID: sub}, KeyID: ukID}); ferr != nil || len(found) == 0 {
			detail = fmt.Sprintf("%s is not in the registry", sub)
		} else if verr := validator.Validate(&model.StepContext{Context: r.Context(), Body: body}, header, found[0].SigningPublicKey, false); verr != nil {
			detail = verr.Error()
		} else {
			ok, detail = true, "signed by "+sub
		}
		seen.add(received{Action: action, MessageID: mid, Verified: &ok, Detail: detail})
		log.Printf("[buyer app] got %s message_id=%s signature verified=%v (%s)", action, mid, ok, detail)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, ackBody)
	})
	log.Printf("[buyer app] listening on :%d", *port)
	return http.ListenAndServe(fmt.Sprintf(":%d", *port), mux)
}

// splitKeyID reads the subscriber id and ukId from keyId="sub|ukId|alg".
func splitKeyID(header string) (sub, ukID string, ok bool) {
	i := strings.Index(header, `keyId="`)
	if i < 0 {
		return "", "", false
	}
	rest := header[i+len(`keyId="`):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return "", "", false
	}
	parts := strings.Split(rest[:end], "|")
	if len(parts) != 3 {
		return "", "", false
	}
	return parts[0], parts[1], true
}
