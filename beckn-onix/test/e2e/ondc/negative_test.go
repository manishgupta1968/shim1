package ondc_e2e_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

// assertNotDelivered checks that a rejected message never reached the app behind ONIX.
func assertNotDelivered(t *testing.T, b *backend, txn string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	if got := b.matching(func(r recorded) bool { id, _, _ := ids(r.Body); return id == txn }); len(got) != 0 {
		t.Errorf("a rejected message reached the app: %s", got[0].Body)
	}
}

// signatureCase is one way a message can fail ONDC signature verification.
type signatureCase struct {
	name       string
	wantCode   string
	wantHeader string // a challenge header the receiver must send back (WWW-Authenticate)
	// send posts the (valid, unsigned) body to url the way this case misbehaves.
	send func(t *testing.T, url string, body []byte, v journeyIDs, signer participant, i *sharedInfra) response
}

func signatureCases() []signatureCase {
	signedAt := func(created, expires time.Time) func(*testing.T, string, []byte, journeyIDs, participant, *sharedInfra) response {
		return func(t *testing.T, url string, body []byte, _ journeyIDs, signer participant, _ *sharedInfra) response {
			return post(t, url, body, map[string]string{model.AuthHeaderSubscriber: signBody(signer, body, created, expires)})
		}
	}
	signedBy := func(pick func(*sharedInfra) participant) func(*testing.T, string, []byte, journeyIDs, participant, *sharedInfra) response {
		return func(t *testing.T, url string, body []byte, _ journeyIDs, _ participant, i *sharedInfra) response {
			return postSigned(t, url, body, pick(i))
		}
	}
	return []signatureCase{
		{
			name: "the body was changed after signing", wantCode: "AUT_SIGNATURE_INVALID",
			send: func(t *testing.T, url string, body []byte, v journeyIDs, signer participant, _ *sharedInfra) response {
				header := signBody(signer, body, time.Now(), time.Now().Add(5*time.Minute))
				tampered := bytes.Replace(body, []byte(v.Timestamp), []byte("2020-01-01T00:00:00.000Z"), 1)
				return post(t, url, tampered, map[string]string{model.AuthHeaderSubscriber: header})
			},
		},
		{
			name: "no Authorization header", wantCode: "AUT_SIGNATURE_MISSING", wantHeader: model.UnaAuthorizedHeaderSubscriber,
			send: func(t *testing.T, url string, body []byte, _ journeyIDs, _ participant, _ *sharedInfra) response {
				return post(t, url, body, nil)
			},
		},
		{
			name: "signed with the wrong private key", wantCode: "AUT_SIGNATURE_INVALID",
			send: func(t *testing.T, url string, body []byte, _ journeyIDs, signer participant, _ *sharedInfra) response {
				_, other, _ := ed25519.GenerateKey(rand.Reader)
				forged := signer
				forged.Priv = other
				return postSigned(t, url, body, forged)
			},
		},
		{
			name: "subscriber is not in the registry", wantCode: "AUT_SUBSCRIBER_NOT_FOUND",
			send: func(t *testing.T, url string, body []byte, _ journeyIDs, _ participant, _ *sharedInfra) response {
				return postSigned(t, url, body, newParticipant("ghost.example.com"))
			},
		},
		{
			name: "signer is a different participant than the one named in the message", wantCode: "AUT_UNAUTHORIZED_ACTION",
			send: signedBy(func(i *sharedInfra) participant { return i.rogue }),
		},
		{
			name: "the signer's key has expired", wantCode: "AUT_KEY_EXPIRED_OR_REVOKED",
			send: signedBy(func(i *sharedInfra) participant { return i.expired }),
		},
		{
			name: "the signer is unsubscribed", wantCode: "AUT_KEY_EXPIRED_OR_REVOKED",
			send: signedBy(func(i *sharedInfra) participant { return i.unsubscribed }),
		},
		{
			name: "the signature has expired", wantCode: "AUT_SIGNATURE_INVALID",
			send: func(t *testing.T, url string, body []byte, v journeyIDs, signer participant, i *sharedInfra) response {
				return signedAt(time.Now().Add(-10*time.Minute), time.Now().Add(-5*time.Minute))(t, url, body, v, signer, i)
			},
		},
		{
			name: "the signature is dated in the future", wantCode: "AUT_SIGNATURE_INVALID",
			send: func(t *testing.T, url string, body []byte, v journeyIDs, signer participant, i *sharedInfra) response {
				return signedAt(time.Now().Add(10*time.Minute), time.Now().Add(15*time.Minute))(t, url, body, v, signer, i)
			},
		},
	}
}

// TestNegativeSignatures sends messages with bad signatures straight to each ONIX receiver. Every one must be
// refused with HTTP 401 and an ONDC NACK (message.ack.status NACK and a top-level error), and must never reach
// the app behind ONIX.
func TestNegativeSignatures(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		directions := []struct {
			name    string
			action  string
			url     string
			signer  participant
			backend *backend
		}{
			{"request to the seller's ONIX", "select", w.bppURL + "/bpp/receiver/select", w.bap, w.bppBackend},
			{"callback to the buyer's ONIX", "on_select", w.bapURL + "/bap/receiver/on_select", w.bpp, w.bapBackend},
		}
		for _, d := range directions {
			d := d
			t.Run(d.name, func(t *testing.T) {
				for _, c := range signatureCases() {
					c := c
					t.Run(c.name, func(t *testing.T) {
						v := w.newIDs()
						body := w.fixture(t, d.action, v)
						resp := c.send(t, d.url, body, v, d.signer, w.infra)
						requireNACK(t, resp, http.StatusUnauthorized, "CONTEXT-ERROR", c.wantCode)
						if c.wantHeader != "" && resp.Header.Get(c.wantHeader) == "" {
							t.Errorf("want a %s challenge header, got none", c.wantHeader)
						}
						assertNotDelivered(t, d.backend, v.Txn)
					})
				}
			})
		}

		t.Run("search with a bad gateway signature", func(t *testing.T) {
			v := w.newIDs()
			body := w.fixture(t, "search", v)
			now := time.Now()
			other := mutate(t, body, func(d map[string]any) { d["context"].(map[string]any)["ttl"] = "PT45S" })
			resp := post(t, w.bppURL+"/bpp/receiver/search", body, map[string]string{
				model.AuthHeaderSubscriber: signBody(w.bap, body, now, now.Add(5*time.Minute)),
				model.AuthHeaderGateway:    signBody(w.bg, other, now, now.Add(5*time.Minute)), // gateway signed a different message
			})
			requireNACK(t, resp, http.StatusUnauthorized, "CONTEXT-ERROR", "AUT_SIGNATURE_INVALID")
			if resp.Header.Get(model.UnaAuthorizedHeaderGateway) == "" {
				t.Errorf("want a %s challenge header, got none", model.UnaAuthorizedHeaderGateway)
			}
			assertNotDelivered(t, w.bppBackend, v.Txn)
		})
	})
}

// path elements are map keys (string) or list positions (int).
func at(doc any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			doc = doc.(map[string]any)[k]
		case int:
			doc = doc.([]any)[k]
		}
	}
	return doc
}

func setAt(doc map[string]any, value any, path ...any) {
	parent := at(doc, path[:len(path)-1]...)
	switch k := path[len(path)-1].(type) {
	case string:
		parent.(map[string]any)[k] = value
	case int:
		parent.([]any)[k] = value
	}
}

func deleteAt(doc map[string]any, path ...any) {
	delete(at(doc, path[:len(path)-1]...).(map[string]any), path[len(path)-1].(string))
}

// schemaCase is a message that breaks an ONDC rule.
type schemaCase struct {
	name     string
	action   string
	receiver bool // also send it, validly signed, to the receiving ONIX
	edit     func(doc map[string]any)
	// wantMessage is text the NACK's error message must contain.
	wantMessage string
}

func trv10SchemaCases() []schemaCase {
	return []schemaCase{
		{name: "select without bpp_id", action: "select", receiver: true, edit: func(d map[string]any) { deleteAt(d, "context", "bpp_id") }},
		{name: "search outside India", action: "search", edit: func(d map[string]any) { setAt(d, "USA", "context", "location", "country", "code") }},
		{name: "search with one payment tag instead of two", action: "search", edit: func(d map[string]any) {
			tags := at(d, "message", "intent", "payment", "tags").([]any)
			setAt(d, tags[:1], "message", "intent", "payment", "tags")
		}},
		{name: "search without ttl", action: "search", edit: func(d map[string]any) { deleteAt(d, "context", "ttl") }},
		{name: "init payment status UNPAID", action: "init", receiver: true, edit: func(d map[string]any) {
			setAt(d, "UNPAID", "message", "order", "payments", 0, "status")
		}},
		{name: "cancel with a non-numeric reason id", action: "cancel", edit: func(d map[string]any) {
			setAt(d, "abc", "message", "cancellation_reason_id")
		}},
		{name: "confirm without the rider's phone number", action: "confirm", edit: func(d map[string]any) {
			deleteAt(d, "message", "order", "fulfillments", 0, "customer", "contact", "phone")
		}},
		{name: "status with a non-string order id", action: "status", edit: func(d map[string]any) { setAt(d, 12345, "message", "order_id") }},
		{name: "extra context key core_version", action: "select", edit: func(d map[string]any) { setAt(d, "1.1.0", "context", "core_version") }},
		{name: "on_search with vehicle BUS", action: "on_search", receiver: true, edit: func(d map[string]any) {
			setAt(d, "BUS", "message", "catalog", "providers", 0, "fulfillments", 0, "vehicle", "category")
		}},
		{name: "on_init with breakup title SURGE", action: "on_init", edit: func(d map[string]any) {
			setAt(d, "SURGE", "message", "order", "quote", "breakup", 0, "title")
		}},
	}
}

func retailSchemaCases() []schemaCase {
	return []schemaCase{
		{name: "search without core_version", action: "search", receiver: true, wantMessage: "missing field Version", edit: func(d map[string]any) {
			deleteAt(d, "context", "core_version")
		}},
		{name: "search with finder fee type flat", action: "search", edit: func(d map[string]any) {
			setAt(d, "flat", "message", "intent", "payment", "@ondc/org/buyer_app_finder_fee_type")
		}},
		{name: "search without payment", action: "search", edit: func(d map[string]any) { deleteAt(d, "message", "intent", "payment") }},
		{name: "select without bpp_id", action: "select", receiver: true, edit: func(d map[string]any) { deleteAt(d, "context", "bpp_id") }},
		{name: "select outside India", action: "select", edit: func(d map[string]any) { setAt(d, "USA", "context", "country") }},
		{name: "init without billing", action: "init", receiver: true, edit: func(d map[string]any) { deleteAt(d, "message", "order", "billing") }},
		{name: "confirm with payment status UNPAID", action: "confirm", edit: func(d map[string]any) {
			setAt(d, "UNPAID", "message", "order", "payment", "status")
		}},
		{name: "cancel without a reason id", action: "cancel", edit: func(d map[string]any) { deleteAt(d, "message", "cancellation_reason_id") }},
		{name: "status without an order id", action: "status", edit: func(d map[string]any) { deleteAt(d, "message", "order_id") }},
		{name: "update with an unknown target", action: "update", edit: func(d map[string]any) { setAt(d, "refund", "message", "update_target") }},
		{name: "on_select without a quote", action: "on_select", receiver: true, edit: func(d map[string]any) {
			deleteAt(d, "message", "order", "quote")
		}},
		{name: "on_confirm without an order id", action: "on_confirm", edit: func(d map[string]any) { deleteAt(d, "message", "order", "id") }},
		{name: "on_status with an unknown order state", action: "on_status", edit: func(d map[string]any) {
			setAt(d, "Teleported", "message", "order", "state")
		}},
		{name: "on_cancel that is not cancelled", action: "on_cancel", edit: func(d map[string]any) { setAt(d, "Completed", "message", "order", "state") }},
	}
}

// TestNegativeSchemas sends messages that break an ONDC rule through ONIX's caller, which must refuse them with
// HTTP 400 and an ONDC NACK before signing or forwarding anything. Cases marked receiver are also sent, validly
// signed, to the receiving ONIX, which must refuse them the same way.
func TestNegativeSchemas(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		cases := trv10SchemaCases()
		if w.spec.name == "retail" {
			cases = retailSchemaCases()
		}
		for _, c := range cases {
			c := c
			t.Run(c.name, func(t *testing.T) {
				v := w.newIDs()
				body := mutate(t, w.fixture(t, c.action, v), c.edit)
				isCallback := strings.HasPrefix(c.action, "on_")

				callerURL, receiverURL, signer, backend := w.bapURL+"/bap/caller/", w.bppURL+"/bpp/receiver/", w.bap, w.bppBackend
				if isCallback {
					callerURL, receiverURL, signer, backend = w.bppURL+"/bpp/caller/", w.bapURL+"/bap/receiver/", w.bpp, w.bapBackend
				}

				resp := post(t, callerURL+c.action, body, nil)
				e := requireNACK(t, resp, http.StatusBadRequest, "JSON-SCHEMA-ERROR", "SCH_INVALID_FORMAT")
				if c.wantMessage != "" && !strings.Contains(e.Message, c.wantMessage) {
					t.Errorf("error message %q does not mention %q", e.Message, c.wantMessage)
				}
				if c.receiver {
					resp = postSigned(t, receiverURL+c.action, body, signer)
					requireNACK(t, resp, http.StatusBadRequest, "JSON-SCHEMA-ERROR", "SCH_INVALID_FORMAT")
				}
				assertNotDelivered(t, backend, v.Txn)
			})
		}
	})
}

// TestNegativeRouting covers messages ONIX has no schema or route for.
func TestNegativeRouting(t *testing.T) {
	eachWorld(t, func(t *testing.T, w *world) {
		t.Run("unknown domain", func(t *testing.T) {
			v := w.newIDs()
			body := mutate(t, w.fixture(t, "select", v), func(d map[string]any) { setAt(d, "ONDC:XYZ99", "context", "domain") })
			resp := post(t, w.bapURL+"/bap/caller/select", body, nil)
			e := requireNACK(t, resp, http.StatusBadRequest, "JSON-SCHEMA-ERROR", "SCH_INVALID_FORMAT")
			if !strings.Contains(e.Message, "schema not found") {
				t.Errorf("error message %q should say the schema was not found", e.Message)
			}
			assertNotDelivered(t, w.bppBackend, v.Txn)
		})
		t.Run("unknown protocol version", func(t *testing.T) {
			v := w.newIDs()
			body := mutate(t, w.fixture(t, "select", v), func(d map[string]any) { setAt(d, "9.9.9", "context", w.spec.versionKey) })
			resp := post(t, w.bapURL+"/bap/caller/select", body, nil)
			requireNACK(t, resp, http.StatusBadRequest, "JSON-SCHEMA-ERROR", "SCH_INVALID_FORMAT")
			assertNotDelivered(t, w.bppBackend, v.Txn)
		})
		t.Run("unknown endpoint", func(t *testing.T) {
			v := w.newIDs()
			resp := post(t, w.bapURL+"/bap/caller/refund", w.fixture(t, "select", v), nil)
			requireNACK(t, resp, http.StatusBadRequest, "JSON-SCHEMA-ERROR", "SCH_INVALID_FORMAT")
			assertNotDelivered(t, w.bppBackend, v.Txn)
		})
		t.Run("empty body", func(t *testing.T) {
			resp := post(t, w.bapURL+"/bap/caller/select", nil, nil)
			if resp.Status != http.StatusBadRequest {
				t.Errorf("empty body: HTTP %d %s, want 400", resp.Status, resp.Body)
			}
		})
		t.Run("body that is not JSON", func(t *testing.T) {
			resp := post(t, w.bapURL+"/bap/caller/select", []byte("not json"), nil)
			if resp.Status != http.StatusBadRequest || !strings.Contains(string(resp.Body), "SCH_INVALID_JSON") {
				t.Errorf("not JSON: HTTP %d %s, want 400 with SCH_INVALID_JSON", resp.Status, resp.Body)
			}
		})
	})
}
