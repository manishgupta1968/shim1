package ondc_e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/google/uuid"
)

// requestActions are the ten Beckn requests a buyer app (BAP) sends; each has an on_* callback from the seller app (BPP).
var requestActions = []string{"search", "select", "init", "confirm", "status", "track", "update", "cancel", "rating", "support"}

func callbackOf(action string) string { return "on_" + action }

func allActions() []string {
	out := append([]string{}, requestActions...)
	for _, a := range requestActions {
		out = append(out, callbackOf(a))
	}
	return out
}

// domainSpec describes one ONDC domain the tests cover.
type domainSpec struct {
	name       string // directory name under testdata/fixtures and testdata/config
	domain     string // context.domain
	versionKey string // context field that carries the protocol version
	version    string // its value
	schemaDir  string // schemas/<schemaDir>/<schemaVersion>
	schemaVer  string
	bapID      string
	bppID      string
	gatewayID  string
}

var domains = []domainSpec{
	{
		name: "trv10", domain: "ONDC:TRV10", versionKey: "version", version: "2.0.0",
		schemaDir: "ondc_trv10", schemaVer: "v2.0.0",
		bapID: "bap.mobility.example.com", bppID: "bpp.mobility.example.com", gatewayID: "gateway.mobility.example.com",
	},
	{
		name: "retail", domain: "ONDC:RET14", versionKey: "core_version", version: "1.2.5",
		schemaDir: "ondc_ret14", schemaVer: "v1.2.5",
		bapID: "bap.retail.example.com", bppID: "bpp.retail.example.com", gatewayID: "gateway.retail.example.com",
	},
}

// world is one ONDC domain's running setup: a buyer-side ONIX, a seller-side ONIX and the mocks around them.
//
//	buyer app ──▶ BAP ONIX /bap/caller/<a> ──sign──▶ BPP ONIX /bpp/receiver/<a> ──verify──▶ seller backend
//	                         (search goes through the gateway)
//	seller app ─▶ BPP ONIX /bpp/caller/on_<a> ─sign─▶ BAP ONIX /bap/receiver/on_<a> ─verify─▶ buyer backend
type world struct {
	spec       domainSpec
	bap, bpp   participant
	bg         participant
	bapURL     string // BAP ONIX, e.g. http://127.0.0.1:41000
	bppURL     string
	bapBackend *backend // the buyer app: receives on_* callbacks
	bppBackend *backend // the seller app: receives requests
	gateway    *gateway
	procs      []*process
	infra      *sharedInfra
}

func (w *world) close() {
	for _, p := range w.procs {
		p.stop()
	}
	if w.gateway != nil {
		w.gateway.close()
	}
	if w.bapBackend != nil {
		w.bapBackend.close()
	}
	if w.bppBackend != nil {
		w.bppBackend.close()
	}
}

type adapterData struct {
	Role, Port, SubscriberID, KeyID              string
	SigningPrivateKey, SigningPublicKey          string
	EncrPrivateKey, EncrPublicKey                string
	PluginDir, SchemaDir, RedisAddr, RegistryURL string
	CallerRouting, ReceiverRouting               string
}

type routingData struct{ GatewayURL, BPPBackendURL, BAPBackendURL string }

func startWorld(i *sharedInfra, spec domainSpec) (*world, error) {
	w := &world{
		spec: spec, infra: i,
		bap: newParticipant(spec.bapID), bpp: newParticipant(spec.bppID), bg: newParticipant(spec.gatewayID),
	}
	i.registry.add(w.bap, "BAP", "SUBSCRIBED")
	i.registry.add(w.bpp, "BPP", "SUBSCRIBED")
	i.registry.add(w.bg, "BG", "SUBSCRIBED")

	bapPort, err := freePort()
	if err != nil {
		return w, err
	}
	bppPort, err := freePort()
	if err != nil {
		return w, err
	}
	w.bapURL = fmt.Sprintf("http://127.0.0.1:%d", bapPort)
	w.bppURL = fmt.Sprintf("http://127.0.0.1:%d", bppPort)
	w.bapBackend = newBackend()
	w.bppBackend = newBackend()
	w.gateway = newGateway(w.bg, w.bppURL+"/bpp/receiver")

	dir := filepath.Join(i.root, "run-"+spec.name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return w, err
	}
	routing := routingData{GatewayURL: w.gateway.URL(), BPPBackendURL: w.bppBackend.URL(), BAPBackendURL: w.bapBackend.URL()}

	for _, side := range []struct {
		role string
		port int
		p    participant
	}{{"bap", bapPort, w.bap}, {"bpp", bppPort, w.bpp}} {
		cfgDir := filepath.Join("testdata", "config", spec.name)
		callerRouting := filepath.Join(dir, side.role+"-caller-routing.yaml")
		receiverRouting := filepath.Join(dir, side.role+"-receiver-routing.yaml")
		if err := renderTemplate(filepath.Join(cfgDir, "routing-"+side.role+"-caller.yaml.tmpl"), callerRouting, routing); err != nil {
			return w, err
		}
		if err := renderTemplate(filepath.Join(cfgDir, "routing-"+side.role+"-receiver.yaml.tmpl"), receiverRouting, routing); err != nil {
			return w, err
		}
		cfgPath := filepath.Join(dir, side.role+".yaml")
		err := renderTemplate(filepath.Join("testdata", "config", "adapter.yaml.tmpl"), cfgPath, adapterData{
			Role: side.role, Port: fmt.Sprint(side.port), SubscriberID: side.p.ID, KeyID: side.p.KeyID,
			SigningPrivateKey: side.p.seedB64(), SigningPublicKey: side.p.pubB64(),
			EncrPrivateKey: side.p.encrB64(), EncrPublicKey: side.p.encrB64(),
			PluginDir: i.pluginDir, SchemaDir: i.schemaDir, RedisAddr: i.redis.Addr(), RegistryURL: i.registry.URL(),
			CallerRouting: callerRouting, ReceiverRouting: receiverRouting,
		})
		if err != nil {
			return w, err
		}
		p, err := startServer(i, spec.name+"-"+side.role, cfgPath, filepath.Join(dir, side.role+".log"), side.port)
		if p != nil {
			w.procs = append(w.procs, p)
		}
		if err != nil {
			return w, err
		}
	}
	return w, nil
}

// logs returns the tail of both ONIX logs, to help diagnose a failed test.
func (w *world) logs() string {
	var b strings.Builder
	for _, p := range w.procs {
		fmt.Fprintf(&b, "--- %s log (last lines) ---\n%s\n", p.name, p.tail(25))
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func fixturePath(spec domainSpec, action string) string {
	return filepath.Join("testdata", "fixtures", spec.name, action+".json")
}

// journeyIDs are the values that change in every message.
type journeyIDs struct {
	Txn, Msg       string
	BAPURI, BPPURI string
	Timestamp      string
}

func timestampNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func (w *world) newIDs() journeyIDs {
	return journeyIDs{
		Txn: uuid.NewString(), Msg: uuid.NewString(),
		BAPURI: w.bapURL + "/bap/receiver", BPPURI: w.bppURL + "/bpp/receiver",
		Timestamp: timestampNow(),
	}
}

func renderFixture(raw []byte, v journeyIDs) []byte {
	return []byte(strings.NewReplacer(
		"{{TRANSACTION_ID}}", v.Txn, "{{MESSAGE_ID}}", v.Msg, "{{TIMESTAMP}}", v.Timestamp,
		"{{BAP_URI}}", v.BAPURI, "{{BPP_URI}}", v.BPPURI,
	).Replace(string(raw)))
}

// fixture returns the rendered message for action with the given ids.
func (w *world) fixture(t testing.TB, action string, v journeyIDs) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(w.spec, action))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return renderFixture(raw, v)
}

// mutate decodes a rendered fixture, lets fn change it, and encodes it again.
func mutate(t testing.TB, body []byte, fn func(doc map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

type response struct {
	Status int
	Header http.Header
	Body   []byte
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func post(t testing.TB, url string, body []byte, headers map[string]string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return response{Status: resp.StatusCode, Header: resp.Header, Body: out}
}

// postSigned signs body as signer with the independent signer and posts it.
func postSigned(t testing.TB, url string, body []byte, signer participant) response {
	t.Helper()
	return post(t, url, body, map[string]string{
		model.AuthHeaderSubscriber: signBody(signer, body, time.Now(), time.Now().Add(5*time.Minute)),
	})
}

// ackStatus returns message.ack.status of an ONDC ACK/NACK body, or "" when the body does not have that shape.
func ackStatus(body []byte) string {
	var m struct {
		Message struct {
			Ack struct {
				Status string `json:"status"`
			} `json:"ack"`
		} `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Message.Ack.Status
}

// ondcError is the top-level error object of an ONDC NACK.
type ondcErrorBody struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

func nackError(body []byte) (ondcErrorBody, bool) {
	var m struct {
		Error *ondcErrorBody `json:"error"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Error == nil {
		return ondcErrorBody{}, false
	}
	return *m.Error, true
}

// requireACK fails the test unless resp is an HTTP 200 with the ONDC ACK body.
func requireACK(t testing.TB, w *world, resp response) {
	t.Helper()
	if resp.Status != http.StatusOK || ackStatus(resp.Body) != "ACK" || !bytes.Equal(bytes.TrimSpace(resp.Body), []byte(ondcAck)) {
		t.Fatalf("want HTTP 200 and %s, got HTTP %d %s\n%s", ondcAck, resp.Status, resp.Body, w.logs())
	}
}

// requireNACK fails the test unless resp is an ONDC NACK with the given HTTP status, error type and code.
func requireNACK(t testing.TB, resp response, status int, typ, code string) ondcErrorBody {
	t.Helper()
	e, ok := nackError(resp.Body)
	if resp.Status != status || ackStatus(resp.Body) != "NACK" || !ok {
		t.Fatalf("want HTTP %d NACK with a top-level error, got HTTP %d %s", status, resp.Status, resp.Body)
	}
	if e.Type != typ || e.Code != code || e.Message == "" {
		t.Fatalf("want error type %q code %q and a message, got %+v", typ, code, e)
	}
	return e
}
