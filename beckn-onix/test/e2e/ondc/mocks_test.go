package ondc_e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
)

// ondcAck is the synchronous answer every ONDC participant gives to an accepted message.
const ondcAck = `{"message":{"ack":{"status":"ACK"}}}`

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// registry stands in for the ONDC registry. ONIX's registry plugin calls POST <url>/lookup with a
// subscription ({subscriber_id, key_id}) and expects the matching subscriptions back.
type registry struct {
	srv *httptest.Server
	mu  sync.Mutex
	rec map[string]model.Subscription
}

func newRegistry() *registry {
	r := &registry{rec: map[string]model.Subscription{}}
	r.srv = httptest.NewServer(http.HandlerFunc(r.handle))
	return r
}

func (r *registry) close() { r.srv.Close() }

func (r *registry) URL() string { return r.srv.URL }

// add registers a participant's signing key with the given subscription status.
func (r *registry) add(p participant, typ, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec[p.ID+"|"+p.KeyID] = model.Subscription{
		Subscriber:       model.Subscriber{SubscriberID: p.ID, Type: typ},
		KeyID:            p.KeyID,
		SigningPublicKey: p.pubB64(),
		EncrPublicKey:    p.encrB64(),
		ValidFrom:        time.Now().AddDate(-1, 0, 0),
		ValidUntil:       time.Now().AddDate(10, 0, 0),
		Status:           status,
	}
}

func (r *registry) handle(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost || req.URL.Path != "/lookup" {
		http.NotFound(w, req)
		return
	}
	var q model.Subscription
	if err := json.NewDecoder(req.Body).Decode(&q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	out := []model.Subscription{}
	if s, ok := r.rec[q.SubscriberID+"|"+q.KeyID]; ok {
		out = append(out, s)
	}
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// ---------------------------------------------------------------------------
// Recording backends (the buyer app and the seller app)
// ---------------------------------------------------------------------------

// recorded is one message a mock received.
type recorded struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// backend is a mock app behind an ONIX receiver. It records everything it receives, answers with an ONDC
// ACK and can call a hook (for example to send the matching callback).
type backend struct {
	srv       *httptest.Server
	mu        sync.Mutex
	reqs      []recorded
	afterACK  func(rec recorded)
	ackStatus int
}

func newBackend() *backend {
	b := &backend{ackStatus: http.StatusOK}
	b.srv = httptest.NewServer(http.HandlerFunc(b.handle))
	return b
}

func (b *backend) close()      { b.srv.Close() }
func (b *backend) URL() string { return b.srv.URL }

func (b *backend) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := recorded{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	b.mu.Lock()
	b.reqs = append(b.reqs, rec)
	hook := b.afterACK
	status := b.ackStatus
	b.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(ondcAck))
	if hook != nil {
		go hook(rec)
	}
}

// setAfterACK installs (or removes, with nil) a hook that runs after the backend has answered a message.
func (b *backend) setAfterACK(f func(rec recorded)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.afterACK = f
}

// matching returns every recorded message for which match is true.
func (b *backend) matching(match func(recorded) bool) []recorded {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []recorded
	for _, r := range b.reqs {
		if match(r) {
			out = append(out, r)
		}
	}
	return out
}

// await waits until a message matching match has been recorded, and returns it.
func (b *backend) await(match func(recorded) bool, timeout time.Duration) (recorded, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if m := b.matching(match); len(m) > 0 {
			return m[0], true
		}
		if time.Now().After(deadline) {
			return recorded{}, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ids pulls the transaction id, message id and action out of a Beckn message body.
func ids(body []byte) (txn, msg, action string) {
	var m struct {
		Context struct {
			TransactionID string `json:"transaction_id"`
			MessageID     string `json:"message_id"`
			Action        string `json:"action"`
		} `json:"context"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Context.TransactionID, m.Context.MessageID, m.Context.Action
}

// forTxn matches a recorded message by transaction id, message id and request path.
func forTxn(txn, msg, path string) func(recorded) bool {
	return func(r recorded) bool {
		t, m, _ := ids(r.Body)
		return t == txn && m == msg && r.Path == path
	}
}

// ---------------------------------------------------------------------------
// Gateway
// ---------------------------------------------------------------------------

// gateway stands in for the ONDC gateway: it receives a signed search from a buyer app, adds its own
// signature in X-Gateway-Authorization and forwards the search, unchanged, to the seller app.
type gateway struct {
	srv  *httptest.Server
	self participant
	bpp  string // URL of the BPP receiver the gateway fans the search out to
	mu   sync.Mutex
	reqs []recorded
}

func newGateway(self participant, bppReceiverURL string) *gateway {
	g := &gateway{self: self, bpp: bppReceiverURL}
	g.srv = httptest.NewServer(http.HandlerFunc(g.handle))
	return g
}

func (g *gateway) close()      { g.srv.Close() }
func (g *gateway) URL() string { return g.srv.URL }

func (g *gateway) received(match func(recorded) bool) []recorded {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []recorded
	for _, r := range g.reqs {
		if match(r) {
			out = append(out, r)
		}
	}
	return out
}

func (g *gateway) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.reqs = append(g.reqs, recorded{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	g.mu.Unlock()
	if r.URL.Path != "/bg/search" {
		http.NotFound(w, r)
		return
	}
	req, err := http.NewRequest(http.MethodPost, g.bpp+"/search", bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(model.AuthHeaderSubscriber, r.Header.Get(model.AuthHeaderSubscriber))
	req.Header.Set(model.AuthHeaderGateway, signBody(g.self, body, time.Now(), time.Now().Add(5*time.Minute)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	for k, v := range resp.Header {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(out)
}
