package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/plugin/definition"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcguard"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcregistry"
)

// provider implements definition.StepProvider for the ondcguard plugin.
type provider struct{}

// New creates the ondcguard step.
//
// Config keys:
//
//	registryMode     stub (default) or http
//	registryFile     stub mode: JSON file with registry entries
//	registryUrl      http mode: registry base URL
//	gatewayActions   comma separated actions that need a gateway signature (default "search")
//	futureSkew       how far in the future a timestamp may be (default "30s")
//	defaultTtl       ttl to use when a request has none (default "5m")
func (provider) New(ctx context.Context, config map[string]string) (definition.Step, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("context cannot be nil")
	}
	cfg := ondcguard.Config{
		Registry: ondcregistry.Config{
			Mode: config["registryMode"],
			File: config["registryFile"],
			URL:  config["registryUrl"],
		},
	}
	if v := config["gatewayActions"]; v != "" {
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				cfg.GatewayActions = append(cfg.GatewayActions, a)
			}
		}
	}
	for key, dst := range map[string]*time.Duration{"futureSkew": &cfg.FutureSkew, "defaultTtl": &cfg.DefaultTTL} {
		if v := config[key]; v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid %s %q: %w", key, v, err)
			}
			*dst = d
		}
	}
	g, err := ondcguard.New(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return g, nil, nil
}

// Provider is the symbol the ONIX plugin manager looks up.
var Provider = provider{}
