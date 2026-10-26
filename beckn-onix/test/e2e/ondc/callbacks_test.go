package ondc_e2e_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

// TestCallbackLegs sends each of the ten on_* callbacks the way a seller app would: unsigned, to the BPP ONIX
// caller. ONIX must validate the message against the ONDC schema, sign it as the seller, send it to the buyer
// app's ONIX (context.bap_uri) where the signature is verified through the registry, and the buyer app must
// receive exactly the message that was sent.
func TestCallbackLegs(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		for _, action := range requestActions {
			cb := callbackOf(action)
			t.Run(cb, func(t *testing.T) {
				v := w.newIDs()
				body := w.fixture(t, cb, v)

				resp := post(t, w.bppURL+"/bpp/caller/"+cb, body, nil)
				requireACK(t, w, resp)

				rec, ok := w.bapBackend.await(forTxn(v.Txn, v.Msg, "/ondc/"+cb), 5*time.Second)
				if !ok {
					t.Fatalf("the buyer app never received %s\n%s", cb, w.logs())
				}
				if !bytes.Equal(rec.Body, body) {
					t.Errorf("the buyer app received a changed message\nsent: %s\ngot:  %s", body, rec.Body)
				}
				if ct := rec.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
				if _, err := verifyAuth(rec.Header.Get(model.AuthHeaderSubscriber), rec.Body, w.bpp, time.Now()); err != nil {
					t.Errorf("Authorization header: %v", err)
				}
				if n := len(w.bapBackend.matching(forTxn(v.Txn, v.Msg, "/ondc/"+cb))); n != 1 {
					t.Errorf("the buyer app received %d copies, want 1", n)
				}
			})
		}
	})
}
