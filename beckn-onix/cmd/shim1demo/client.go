package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// runner sends requests to Shim 1 the way a buyer app and the Gateway would.
type runner struct {
	onix, backend, bap string
	keys               demoKeys
	http               *http.Client
}

// req describes one request. The defaults describe a good /search.
type req struct {
	ctx     map[string]any
	message map[string]any
	sender  participant  // signs Authorization
	gateway *participant // signs X-Gateway-Authorization; nil = header left out
	expired bool         // sign with a validity window that has already ended
	tamper  func(message map[string]any)
}

// sent is what came back.
type sent struct {
	status    int
	body      string
	messageID string
}

// scenario is one test case with its expected outcome.
type scenario struct {
	name         string
	run          func(r *runner) (*sent, error)
	wantStatus   int
	wantCode     string // "" means the request must be accepted (ACK)
	wantType     string
	wantMsg      string // text the error message must contain
	reachBackend bool   // should the request get through to the seller backend?
	callback     bool   // should a signed on_search reach the buyer app?
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newReq returns a good request for action, from the good buyer. A search also
// carries the gateway signature; other actions go straight to the seller.
func (r *runner) newReq(action string) req {
	c := map[string]any{
		"domain": domain, "country": "IND", "city": "std:080",
		"action": action, "core_version": "1.2.0",
		"bap_id": r.keys.Buyer.ID, "bap_uri": buyerURI,
		"transaction_id": uuid(), "message_id": uuid(),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "ttl": "PT30S",
	}
	q := req{ctx: c, message: map[string]any{}, sender: r.keys.Buyer}
	if action == "search" {
		q.message = map[string]any{"intent": map[string]any{"item": map[string]any{"descriptor": map[string]any{"name": "coffee"}}}}
		q.gateway = &r.keys.Gateway
	} else {
		c["bpp_id"], c["bpp_uri"] = sellerID, sellerURI
	}
	return q
}

// send signs and posts q to the seller receiver of Shim 1.
func (r *runner) send(q req) (*sent, error) {
	body, err := json.Marshal(map[string]any{"context": q.ctx, "message": q.message})
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	created, expires := now, now+300
	if q.expired {
		created, expires = now-600, now-300
	}
	headers := map[string]string{"Content-Type": "application/json"}
	if headers["Authorization"], err = q.sender.authHeader(body, created, expires); err != nil {
		return nil, err
	}
	if q.gateway != nil {
		if headers["X-Gateway-Authorization"], err = q.gateway.authHeader(body, created, expires); err != nil {
			return nil, err
		}
	}
	if q.tamper != nil { // change the message AFTER signing
		q.tamper(q.message)
		if body, err = json.Marshal(map[string]any{"context": q.ctx, "message": q.message}); err != nil {
			return nil, err
		}
	}
	action, _ := q.ctx["action"].(string)
	httpReq, err := http.NewRequest(http.MethodPost, r.onix+"/bpp/receiver/"+action, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := r.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	mid, _ := q.ctx["message_id"].(string)
	return &sent{status: resp.StatusCode, body: strings.TrimSpace(string(raw)), messageID: mid}, nil
}

// one returns a scenario function that sends a single request built by mod.
func one(action string, mod func(r *runner, q *req)) func(r *runner) (*sent, error) {
	return func(r *runner) (*sent, error) {
		q := r.newReq(action)
		if mod != nil {
			mod(r, &q)
		}
		return r.send(q)
	}
}

// asSender makes q come from p. The bap_id is set to match, so the request fails
// only for the reason under test (and not on the sender-identity check).
func asSender(p participant) func(r *runner, q *req) {
	return func(r *runner, q *req) { q.sender = p; q.ctx["bap_id"] = p.ID }
}

func (r *runner) scenarios() []scenario {
	return []scenario{
		{name: "valid_search", run: one("search", nil), wantStatus: 200, reachBackend: true, callback: true},
		{name: "tampered_body", run: one("search", func(_ *runner, q *req) {
			q.tamper = func(m map[string]any) { m["intent"] = map[string]any{"item": "something else"} }
		}), wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR", wantMsg: "signature verification failed"},
		{name: "expired_signature", run: one("search", func(_ *runner, q *req) { q.expired = true }),
			wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR"},
		{name: "missing_gateway_header", run: one("search", func(_ *runner, q *req) { q.gateway = nil }),
			wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR", wantMsg: "X-Gateway-Authorization is required"},
		{name: "gateway_not_registered_as_BG", run: one("search", func(r *runner, q *req) { q.gateway = &r.keys.Buyer }),
			wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR", wantMsg: "not registered as BG"},
		{name: "sender_not_in_registry", run: one("search", func(r *runner, q *req) { asSender(r.keys.Stranger)(r, q) }),
			wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR"},
		{name: "sender_not_SUBSCRIBED", run: one("search", func(r *runner, q *req) { asSender(r.keys.Pending)(r, q) }),
			wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR"},
		{name: "sender_registration_lapsed", run: one("search", func(r *runner, q *req) { asSender(r.keys.Lapsed)(r, q) }),
			wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR"},
		{name: "sender_registered_as_seller", run: one("search", func(r *runner, q *req) { asSender(r.keys.WrongType)(r, q) }),
			wantStatus: 401, wantCode: "30016", wantType: "CORE-ERROR", wantMsg: "not registered as BAP"},
		{name: "wrong_bpp_id", run: one("select", func(_ *runner, q *req) { q.ctx["bpp_id"] = "someone.else.example" }),
			wantStatus: 400, wantCode: "30000", wantType: "POLICY-ERROR", wantMsg: "not this seller"},
		{name: "unsupported_city", run: one("search", func(_ *runner, q *req) { q.ctx["city"] = "std:999" }),
			wantStatus: 400, wantCode: "30000", wantType: "POLICY-ERROR", wantMsg: "city std:999"},
		{name: "unsupported_domain", run: one("search", func(_ *runner, q *req) { q.ctx["domain"] = "ONDC:RET99" }),
			wantStatus: 400, wantCode: "30000", wantType: "POLICY-ERROR", wantMsg: "domain ONDC:RET99"},
		{name: "unsupported_core_version", run: one("search", func(_ *runner, q *req) { q.ctx["core_version"] = "1.0.0" }),
			wantStatus: 400, wantCode: "30000", wantType: "POLICY-ERROR", wantMsg: "core_version 1.0.0"},
		{name: "missing_context_field", run: one("search", func(_ *runner, q *req) { delete(q.ctx, "transaction_id") }),
			wantStatus: 400, wantCode: "30000", wantType: "POLICY-ERROR", wantMsg: "transaction_id is required"},
		{name: "missing_search_intent", run: one("search", func(_ *runner, q *req) { q.message = map[string]any{} }),
			wantStatus: 400, wantCode: "30000", wantType: "JSON-SCHEMA-ERROR"},
		{name: "stale_request", run: one("search", func(_ *runner, q *req) {
			q.ctx["timestamp"] = time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339Nano)
		}), wantStatus: 400, wantCode: "30022", wantType: "CONTEXT-ERROR", wantMsg: "stale request"},
		{name: "replayed_message_id", run: func(r *runner) (*sent, error) {
			q := r.newReq("select")
			first, err := r.send(q)
			if err != nil {
				return nil, err
			}
			if first.status != 200 {
				return nil, fmt.Errorf("the first request should pass but got %d %s", first.status, first.body)
			}
			return r.send(q) // same message_id again (fresh signature)
		}, wantStatus: 400, wantCode: "30000", wantType: "POLICY-ERROR", wantMsg: "duplicate", reachBackend: false},
	}
}

// count asks a stub how many requests it has seen.
func (r *runner) count(base, path string) (int, error) {
	items, err := r.list(base + path)
	return len(items), err
}

func (r *runner) list(url string) ([]received, error) {
	resp, err := r.http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var items []received
	return items, json.NewDecoder(resp.Body).Decode(&items)
}

// waitForCallback waits until the buyer app has received a correctly signed
// on_search for messageID.
func (r *runner) waitForCallback(messageID string) (bool, string) {
	deadline := time.Now().Add(5 * time.Second)
	last := "no on_search arrived"
	for time.Now().Before(deadline) {
		items, err := r.list(r.bap + "/bap/received")
		if err == nil {
			for _, it := range items {
				if it.MessageID == messageID && it.Action == "on_search" {
					if it.Verified != nil && *it.Verified {
						return true, it.Detail
					}
					last = "on_search arrived but signature check failed: " + it.Detail
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false, last
}

// check compares what happened with what the scenario expects.
func (r *runner) check(s scenario, got *sent, backendDelta int) (bool, string) {
	var problems []string
	if got.status != s.wantStatus {
		problems = append(problems, fmt.Sprintf("HTTP %d, want %d", got.status, s.wantStatus))
	}
	var doc struct {
		Message struct {
			Ack struct {
				Status string `json:"status"`
			} `json:"ack"`
			Error json.RawMessage `json:"error"`
		} `json:"message"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(got.body), &doc); err != nil {
		problems = append(problems, "answer is not JSON: "+got.body)
	} else if s.wantCode == "" {
		if doc.Message.Ack.Status != "ACK" {
			problems = append(problems, "want ACK, got "+got.body)
		}
	} else {
		if doc.Message.Ack.Status != "NACK" {
			problems = append(problems, "ack.status is "+doc.Message.Ack.Status+", want NACK")
		}
		if len(doc.Message.Error) > 0 {
			problems = append(problems, "error is inside message; ONDC wants it next to message")
		}
		if doc.Error.Code != s.wantCode || doc.Error.Type != s.wantType {
			problems = append(problems, fmt.Sprintf("error is %s/%s, want %s/%s", doc.Error.Type, doc.Error.Code, s.wantType, s.wantCode))
		}
		if !strings.Contains(doc.Error.Message, s.wantMsg) {
			problems = append(problems, fmt.Sprintf("error message %q lacks %q", doc.Error.Message, s.wantMsg))
		}
	}
	wantBackend := 0
	if s.reachBackend {
		wantBackend = 1
	}
	if s.name == "replayed_message_id" {
		wantBackend = 1 // only the first of the two requests may get through
	}
	if backendDelta != wantBackend {
		problems = append(problems, fmt.Sprintf("seller backend saw %d request(s), want %d", backendDelta, wantBackend))
	}
	if s.callback {
		if ok, detail := r.waitForCallback(got.messageID); !ok {
			problems = append(problems, detail)
		}
	}
	return len(problems) == 0, strings.Join(problems, "; ")
}

func runClient(args []string) error {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	onix := fs.String("onix", "http://localhost:8081", "Shim 1 base URL")
	backend := fs.String("backend", "http://localhost:9001", "stub seller backend URL")
	bap := fs.String("bap", "http://localhost:9002", "stub buyer app URL")
	keysFile := fs.String("keys", "config/shim1/generated/keys.json", "keys.json written by `shim1demo keys`")
	only := fs.String("only", "", "run just this scenario")
	verbose := fs.Bool("verbose", false, "print the full answer of every scenario")
	fs.Parse(args)

	r := &runner{onix: *onix, backend: *backend, bap: *bap, http: &http.Client{Timeout: 10 * time.Second}}
	raw, err := os.ReadFile(*keysFile)
	if err != nil {
		return fmt.Errorf("read keys (run `shim1demo keys` first): %w", err)
	}
	if err := json.Unmarshal(raw, &r.keys); err != nil {
		return err
	}
	for _, u := range []string{r.onix + "/health", r.backend + "/ondc/received", r.bap + "/bap/received"} {
		resp, err := r.http.Get(u)
		if err != nil {
			return fmt.Errorf("%s is not reachable: %w", u, err)
		}
		resp.Body.Close()
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tRESULT\tSCENARIO\tEXPECTED\tGOT")
	failed, n := 0, 0
	var details []string
	for _, s := range r.scenarios() {
		if *only != "" && s.name != *only {
			continue
		}
		n++
		before, err := r.count(r.backend, "/ondc/received")
		if err != nil {
			return err
		}
		got, err := s.run(r)
		if err != nil {
			failed++
			fmt.Fprintf(tw, "%d\tFAIL\t%s\t\tcould not run: %v\n", n, s.name, err)
			continue
		}
		after, _ := r.count(r.backend, "/ondc/received")
		ok, why := r.check(s, got, after-before)

		want := fmt.Sprintf("HTTP %d ACK", s.wantStatus)
		if s.wantCode != "" {
			want = fmt.Sprintf("HTTP %d NACK %s %s", s.wantStatus, s.wantCode, s.wantType)
		}
		result, gotText := "PASS", brief(got.body)
		if !ok {
			result, gotText = "FAIL", why
			failed++
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", n, result, s.name, want, gotText)
		if *verbose {
			details = append(details, fmt.Sprintf("%d. %s\n   %s", n, s.name, got.body))
		}
	}
	tw.Flush()
	if len(details) > 0 {
		fmt.Println("\nFull answers:\n" + strings.Join(details, "\n"))
	}
	fmt.Printf("\n%d scenarios, %d passed, %d failed\n", n, n-failed, failed)
	if failed > 0 {
		return fmt.Errorf("%d scenario(s) failed", failed)
	}
	return nil
}

// brief shortens an answer for the result table.
func brief(body string) string {
	if len(body) > 90 {
		return body[:90] + "..."
	}
	return body
}
