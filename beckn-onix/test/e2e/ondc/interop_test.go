package ondc_e2e_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

// TestInteropIndependentSigner sends every message straight to ONIX's receiver, signed by the test's own
// ONDC signer (ondcsign_test.go, no ONIX code). The BPP receiver must accept the ten requests as if the buyer
// app's ONIX had signed them, and the BAP receiver the ten callbacks as if the seller's ONIX had. Together with
// the leg tests (which verify ONIX's signatures with the same independent code) this shows ONIX speaks the
// ONDC signature format in both directions.
func TestInteropIndependentSigner(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		for _, action := range requestActions {
			action := action
			t.Run(action, func(t *testing.T) {
				v := w.newIDs()
				body := w.fixture(t, action, v)
				resp := postSigned(t, w.bppURL+"/bpp/receiver/"+action, body, w.bap)
				requireACK(t, w, resp)
				rec, ok := w.bppBackend.await(forTxn(v.Txn, v.Msg, "/ondc/"+action), 5*time.Second)
				if !ok || !bytes.Equal(rec.Body, body) {
					t.Fatalf("the seller app did not receive the message unchanged (received=%v)\n%s", ok, w.logs())
				}
			})
		}
		for _, action := range requestActions {
			cb := callbackOf(action)
			t.Run(cb, func(t *testing.T) {
				v := w.newIDs()
				body := w.fixture(t, cb, v)
				resp := postSigned(t, w.bapURL+"/bap/receiver/"+cb, body, w.bpp)
				requireACK(t, w, resp)
				rec, ok := w.bapBackend.await(forTxn(v.Txn, v.Msg, "/ondc/"+cb), 5*time.Second)
				if !ok || !bytes.Equal(rec.Body, body) {
					t.Fatalf("the buyer app did not receive the message unchanged (received=%v)\n%s", ok, w.logs())
				}
			})
		}
		// A search that arrives through the gateway carries two signatures: the buyer's and the gateway's.
		t.Run("search via gateway", func(t *testing.T) {
			v := w.newIDs()
			body := w.fixture(t, "search", v)
			now := time.Now()
			resp := post(t, w.bppURL+"/bpp/receiver/search", body, map[string]string{
				model.AuthHeaderSubscriber: signBody(w.bap, body, now, now.Add(5*time.Minute)),
				model.AuthHeaderGateway:    signBody(w.bg, body, now, now.Add(5*time.Minute)),
			})
			requireACK(t, w, resp)
			if _, ok := w.bppBackend.await(forTxn(v.Txn, v.Msg, "/ondc/search"), 5*time.Second); !ok {
				t.Fatalf("the seller app did not receive the search\n%s", w.logs())
			}
		})
	})
}
