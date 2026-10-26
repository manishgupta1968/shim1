package ondc_e2e_test

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/google/uuid"
)

// TestJourney plays one whole order: the buyer app sends the ten requests in the order of a real purchase
// (search, select, init, confirm, status, track, update, cancel, rating, support) and the seller app answers
// each with its on_* callback in the same transaction and with the same message id, as ONDC requires. The
// seller app also sends two unsolicited callbacks (on_status and on_update with fresh message ids), which a
// BPP is allowed to push at any time.
func TestJourney(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		callbackErrs := make(chan error, 64)
		w.bppBackend.setAfterACK(func(rec recorded) {
			txn, msg, action := ids(rec.Body)
			if !contains(requestActions, action) {
				return
			}
			cb := callbackOf(action)
			raw, err := os.ReadFile(fixturePath(w.spec, cb))
			if err != nil {
				callbackErrs <- err
				return
			}
			body := renderFixture(raw, journeyIDs{Txn: txn, Msg: msg, BAPURI: w.bapURL + "/bap/receiver", BPPURI: w.bppURL + "/bpp/receiver", Timestamp: timestampNow()})
			resp := postNoFatal(w.bppURL+"/bpp/caller/"+cb, body)
			if resp.Status != http.StatusOK || ackStatus(resp.Body) != "ACK" {
				callbackErrs <- fmt.Errorf("seller app's %s: HTTP %d %s", cb, resp.Status, resp.Body)
			}
		})
		defer w.bppBackend.setAfterACK(nil)

		v := w.newIDs() // one transaction for the whole order
		for _, action := range requestActions {
			v.Msg = uuid.NewString()
			v.Timestamp = timestampNow()
			cb := callbackOf(action)

			resp := post(t, w.bapURL+"/bap/caller/"+action, w.fixture(t, action, v), nil)
			requireACK(t, w, resp)

			rec, ok := w.bapBackend.await(forTxn(v.Txn, v.Msg, "/ondc/"+cb), 5*time.Second)
			if !ok {
				t.Fatalf("%s: no %s reached the buyer app in the same transaction with the same message id\n%s", action, cb, w.logs())
			}
			txn, msg, got := ids(rec.Body)
			if txn != v.Txn || msg != v.Msg || got != cb {
				t.Errorf("%s: callback ids (%s, %s, %s) do not match the request (%s, %s, %s)", action, txn, msg, got, v.Txn, v.Msg, cb)
			}
			if _, err := verifyAuth(rec.Header.Get(model.AuthHeaderSubscriber), rec.Body, w.bpp, time.Now()); err != nil {
				t.Errorf("%s: callback signature: %v", cb, err)
			}
		}

		// Unsolicited callbacks: nobody asked, so they carry new message ids.
		for _, cb := range []string{"on_status", "on_update"} {
			u := w.newIDs()
			u.Txn = v.Txn
			body := w.fixture(t, cb, u)
			requireACK(t, w, post(t, w.bppURL+"/bpp/caller/"+cb, body, nil))
			rec, ok := w.bapBackend.await(forTxn(u.Txn, u.Msg, "/ondc/"+cb), 5*time.Second)
			if !ok || !bytes.Equal(rec.Body, body) {
				t.Errorf("unsolicited %s did not reach the buyer app unchanged", cb)
			}
		}

		select {
		case err := <-callbackErrs:
			t.Fatalf("the seller app's callback failed: %v", err)
		default:
		}
		// The buyer app received every callback of the order exactly once.
		for _, action := range requestActions {
			n := len(w.bapBackend.matching(func(r recorded) bool {
				txn, _, a := ids(r.Body)
				return txn == v.Txn && a == callbackOf(action)
			}))
			want := 1
			if action == "status" || action == "update" {
				want = 2 // the solicited one and the unsolicited one
			}
			if n != want {
				t.Errorf("the buyer app received %d %s callbacks in this transaction, want %d", n, callbackOf(action), want)
			}
		}
	})
}

// postNoFatal posts from a goroutine, where t.Fatalf must not be called.
func postNoFatal(url string, body []byte) response {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return response{Status: -1, Body: []byte(err.Error())}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return response{Status: -1, Body: []byte(err.Error())}
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return response{Status: resp.StatusCode, Header: resp.Header, Body: buf.Bytes()}
}
