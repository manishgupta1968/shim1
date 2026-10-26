package ondc_e2e_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

// eachWorld runs fn once per ONDC domain.
func eachWorld(t *testing.T, fn func(t *testing.T, w *world)) {
	t.Helper()
	i := requireInfra(t)
	for _, w := range i.worlds {
		w := w
		t.Run(w.spec.name, func(t *testing.T) { fn(t, w) })
	}
}

// TestRequestLegs sends each of the ten Beckn requests the way a buyer app would: unsigned, to the BAP ONIX
// caller. ONIX must validate the message against the ONDC schema, sign it, route it (search through the
// gateway, the rest to the seller) and the seller app must receive exactly the message that was sent, with a
// signature that verifies independently against the registry.
func TestRequestLegs(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		for _, action := range requestActions {
			action := action
			t.Run(action, func(t *testing.T) {
				v := w.newIDs()
				body := w.fixture(t, action, v)

				resp := post(t, w.bapURL+"/bap/caller/"+action, body, nil)
				requireACK(t, w, resp)

				rec, ok := w.bppBackend.await(forTxn(v.Txn, v.Msg, "/ondc/"+action), 5*time.Second)
				if !ok {
					t.Fatalf("the seller app never received %s\n%s", action, w.logs())
				}
				if !bytes.Equal(rec.Body, body) {
					t.Errorf("the seller app received a changed message\nsent: %s\ngot:  %s", body, rec.Body)
				}
				if ct := rec.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				// The buyer app's own ONIX signed it: verify with the independent verifier, as the BAP.
				if _, err := verifyAuth(rec.Header.Get(model.AuthHeaderSubscriber), rec.Body, w.bap, time.Now()); err != nil {
					t.Errorf("Authorization header: %v", err)
				}
				if n := len(w.bppBackend.matching(forTxn(v.Txn, v.Msg, "/ondc/"+action))); n != 1 {
					t.Errorf("the seller app received %d copies, want 1", n)
				}

				// search is broadcast through the gateway, which adds its own signature.
				viaGateway := w.gateway.received(forTxn(v.Txn, v.Msg, "/bg/search"))
				if action == "search" {
					if len(viaGateway) != 1 {
						t.Fatalf("the gateway saw %d searches, want 1", len(viaGateway))
					}
					if _, err := verifyAuth(rec.Header.Get(model.AuthHeaderGateway), rec.Body, w.bg, time.Now()); err != nil {
						t.Errorf("X-Gateway-Authorization header: %v", err)
					}
				} else {
					if len(viaGateway) != 0 {
						t.Errorf("the gateway saw %d %s messages, want 0", len(viaGateway), action)
					}
					if h := rec.Header.Get(model.AuthHeaderGateway); h != "" {
						t.Errorf("unexpected X-Gateway-Authorization on %s: %s", action, h)
					}
				}
			})
		}
	})
}

// TestRequestLegsReturnBackendAnswer checks that ONIX hands the seller app's own answer back to the buyer
// app unchanged (a request is acknowledged by the seller app, not by ONIX).
func TestRequestLegsReturnBackendAnswer(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		w.bppBackend.mu.Lock()
		w.bppBackend.ackStatus = http.StatusOK
		w.bppBackend.mu.Unlock()
		v := w.newIDs()
		resp := post(t, w.bapURL+"/bap/caller/select", w.fixture(t, "select", v), nil)
		requireACK(t, w, resp)
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
			t.Errorf("Content-Type = %q, want application/json", resp.Header.Get("Content-Type"))
		}
	})
}
