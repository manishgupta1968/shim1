// Package ondc_e2e_test holds the end-to-end tests that send realistic ONDC messages through real ONIX
// processes: every Beckn endpoint (search, select, init, confirm, status, track, update, cancel, rating,
// support and their on_* callbacks) for ONDC TRV10 (mobility) and ONDC Retail (RET14).
//
// What runs:
//   - the real `server` binary and the real plugins (.so), built from this module;
//   - two ONIX processes per ONDC domain, one for the buyer app (BAP) and one for the seller app (BPP);
//   - in this test process: Redis (miniredis), the registry, the gateway and the two app backends.
//
// See README.md for how to run the tests and how the fixtures were made.
package ondc_e2e_test

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

// infra is the running world shared by all tests. It is nil with -short.
var (
	infra    *sharedInfra
	infraErr error
	// tmpRoot holds everything the tests create: schemas, rendered configs, logs, built binaries.
	tmpRoot string
)

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	var err error
	tmpRoot, err = os.MkdirTemp("", "onix-ondc-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot create temp dir: %v\n", err)
		return 1
	}
	if os.Getenv("ONIX_E2E_KEEP") != "" {
		fmt.Printf("ONIX_E2E_KEEP is set: keeping %s\n", tmpRoot)
	} else {
		defer os.RemoveAll(tmpRoot)
	}

	if !testing.Short() {
		infra, infraErr = setupInfra(tmpRoot)
		if infra != nil {
			defer infra.close()
		}
		if infraErr != nil {
			fmt.Fprintf(os.Stderr, "end-to-end setup failed: %v\n", infraErr)
		}
	}
	return m.Run()
}

// requireInfra returns the shared infrastructure, skipping the test with -short and failing it when
// the setup in TestMain did not succeed.
func requireInfra(t *testing.T) *sharedInfra {
	t.Helper()
	if testing.Short() {
		t.Skip("end-to-end tests are skipped with -short")
	}
	if infraErr != nil || infra == nil {
		t.Fatalf("end-to-end setup failed: %v", infraErr)
	}
	return infra
}
