package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/plugin/definition"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcregistry"
)

// provider implements definition.RegistryLookupProvider for the ondcregistry plugin.
type provider struct{}

// New creates the registry client.
//
// Config keys: mode (stub|http, default stub), file (stub), url (http), timeout (http, e.g. "5s"),
// callerId, callerKeyId, callerPrivateKey (http: our identity, used to sign the lookup request),
// cacheTTL (http: how long a registry answer is reused, default "1m"; "0s" switches the cache off).
func (provider) New(ctx context.Context, _ definition.Cache, config map[string]string) (definition.RegistryLookup, func() error, error) {
	if ctx == nil {
		return nil, nil, errors.New("context cannot be nil")
	}
	cfg := ondcregistry.Config{
		Mode:             config["mode"],
		File:             config["file"],
		URL:              config["url"],
		CallerID:         config["callerId"],
		CallerKeyID:      config["callerKeyId"],
		CallerPrivateKey: config["callerPrivateKey"],
	}
	if t := config["timeout"]; t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid timeout %q: %w", t, err)
		}
		cfg.Timeout = d
	}
	if t := config["cacheTTL"]; t != "" {
		d, err := time.ParseDuration(t)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid cacheTTL %q: %w", t, err)
		}
		if d == 0 {
			d = -1 // "0s" means: no cache
		}
		cfg.CacheTTL = d
	}
	c, err := ondcregistry.New(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return c, nil, nil
}

// Provider is the symbol the ONIX plugin manager looks up.
var Provider = provider{}
