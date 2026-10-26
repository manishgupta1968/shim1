package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcregistry"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/signvalidator"
)

// registryStats is what the stub registry has seen. The demo script reads it from
// GET /registry/stats to prove that Shim 1 really asked the registry, with signed requests.
type registryStats struct {
	Lookups          int `json:"lookups"`
	SignedOK         int `json:"signed_ok"`
	RefusedUnsigned  int `json:"refused_unsigned"`
	RefusedBadSigner int `json:"refused_bad_signature"`

	// The last accepted lookup, exactly as received, so that an independent checker
	// (ONDC's own sample code) can verify the signature itself.
	LastAuthorization string `json:"last_authorization,omitempty"`
	LastBody          string `json:"last_body,omitempty"`
}

// runRegistry is a stand-in for the ONDC Registry's /v2.0/lookup, over HTTP.
// Like the real registry it only answers a lookup that is signed by a registered
// participant. It returns every entry that matches, including entries that are not
// SUBSCRIBED or are out of date: deciding what is usable is Shim 1's job.
func runRegistry(args []string) error {
	fs := flag.NewFlagSet("registry", flag.ExitOnError)
	port := fs.Int("port", 9003, "listen port")
	file := fs.String("registry", "config/shim1/generated/registry.json", "registry entries file")
	fs.Parse(args)

	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var entries []ondcregistry.Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	validator, _, err := signvalidator.New(context.Background(), &signvalidator.Config{})
	if err != nil {
		return err
	}

	var mu sync.Mutex
	var stats registryStats
	bump := func(f func(*registryStats)) { mu.Lock(); defer mu.Unlock(); f(&stats) }

	mux := http.NewServeMux()
	mux.HandleFunc("/registry/stats", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(stats)
	})
	mux.HandleFunc("/v2.0/lookup", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bump(func(s *registryStats) { s.Lookups++ })

		header := r.Header.Get("Authorization")
		sub, ukID, ok := splitKeyID(header)
		if !ok {
			bump(func(s *registryStats) { s.RefusedUnsigned++ })
			log.Printf("[registry] refused: lookup is not signed")
			http.Error(w, `{"message":{"ack":{"status":"NACK"}},"error":{"code":"401","message":"signature required"}}`, http.StatusUnauthorized)
			return
		}
		var caller *ondcregistry.Entry
		for i := range entries {
			if entries[i].SubscriberID == sub && entries[i].UkID == ukID && entries[i].Status == "SUBSCRIBED" {
				caller = &entries[i]
			}
		}
		if caller == nil {
			bump(func(s *registryStats) { s.RefusedBadSigner++ })
			http.Error(w, `{"error":{"code":"401","message":"caller is not a subscribed participant"}}`, http.StatusUnauthorized)
			return
		}
		if err := validator.Validate(&model.StepContext{Context: r.Context(), Body: body}, header, caller.SigningPublicKey, false); err != nil {
			bump(func(s *registryStats) { s.RefusedBadSigner++ })
			log.Printf("[registry] refused: bad signature from %s: %v", sub, err)
			http.Error(w, `{"error":{"code":"401","message":"invalid signature"}}`, http.StatusUnauthorized)
			return
		}
		bump(func(s *registryStats) { s.SignedOK++; s.LastAuthorization = header; s.LastBody = string(body) })

		var q ondcregistry.Query
		if err := json.Unmarshal(body, &q); err != nil {
			http.Error(w, `{"error":{"code":"400","message":"bad request"}}`, http.StatusBadRequest)
			return
		}
		out := []ondcregistry.Entry{}
		for _, e := range entries {
			if ondcregistry.Match(e, q) {
				out = append(out, e)
			}
		}
		log.Printf("[registry] %s asked for %+v, answered with %d entries", sub, q, len(out))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	log.Printf("[registry] listening on :%d (answers POST /v2.0/lookup only when it is signed)", *port)
	return http.ListenAndServe(fmt.Sprintf(":%d", *port), mux)
}
