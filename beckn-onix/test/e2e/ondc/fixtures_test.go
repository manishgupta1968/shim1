package ondc_e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/schemavalidator"
	"gopkg.in/yaml.v3"
)

// selfCheckIDs renders fixtures with fixed, well-formed values; the self-checks need no running ONIX.
var selfCheckIDs = journeyIDs{
	Txn:       "7afe44fd-d947-4a0a-81bc-d286784df2c1",
	Msg:       "0d3d2c49-050a-4018-ace9-24ede7de512e",
	Timestamp: "2026-07-10T10:00:00.000Z",
	BAPURI:    "http://127.0.0.1:9001/bap/receiver",
	BPPURI:    "http://127.0.0.1:9002/bpp/receiver",
}

func readRendered(t *testing.T, spec domainSpec, action string) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixturePath(spec, action))
	if err != nil {
		t.Fatalf("read fixture %s/%s: %v", spec.name, action, err)
	}
	return renderFixture(raw, selfCheckIDs)
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return doc
}

func lookup(doc any, path ...string) any {
	for _, p := range path {
		m, ok := doc.(map[string]any)
		if !ok {
			return nil
		}
		doc = m[p]
	}
	return doc
}

func str(v any) string { s, _ := v.(string); return s }

// TestFixturesAreONDCValid proves the fixtures are real ONDC messages, independent of ONIX's routing and
// signing: every one validates against the ONDC schemas ONIX ships (the same schemavalidator ONIX runs), and
// the messages of one domain tell one consistent story.
func TestFixturesAreONDCValid(t *testing.T) {
	dir := extractedSchemas(t)
	validator, _, err := schemavalidator.New(context.Background(), &schemavalidator.Config{SchemaDir: dir})
	if err != nil {
		t.Fatalf("load schemas: %v", err)
	}

	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	for _, spec := range domains {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			orderIDs, providerIDs := map[string]bool{}, map[string]bool{}
			for _, action := range allActions() {
				body := readRendered(t, spec, action)
				doc := decode(t, body)
				ctx := lookup(doc, "context").(map[string]any)

				// 1. The ONDC schema accepts it.
				if err := validator.Validate(context.Background(), &url.URL{Path: action}, body); err != nil {
					t.Errorf("%s: %v", action, err)
				}

				// 2. The context is what ONDC requires for this domain.
				if str(ctx["action"]) != action {
					t.Errorf("%s: context.action = %q", action, ctx["action"])
				}
				if str(ctx["domain"]) != spec.domain || str(ctx[spec.versionKey]) != spec.version {
					t.Errorf("%s: domain/%s = %v/%v, want %s/%s", action, spec.versionKey, ctx["domain"], ctx[spec.versionKey], spec.domain, spec.version)
				}
				if str(ctx["bap_id"]) != spec.bapID {
					t.Errorf("%s: bap_id = %v, want %s", action, ctx["bap_id"], spec.bapID)
				}
				if action != "search" && str(ctx["bpp_id"]) != spec.bppID {
					t.Errorf("%s: bpp_id = %v, want %s", action, ctx["bpp_id"], spec.bppID)
				}
				if action == "search" && ctx["bpp_id"] != nil {
					t.Errorf("search is a broadcast and must not name a bpp_id")
				}
				if !uuidRe.MatchString(str(ctx["transaction_id"])) || !uuidRe.MatchString(str(ctx["message_id"])) {
					t.Errorf("%s: transaction_id/message_id must be UUIDs", action)
				}
				if _, err := time.Parse(time.RFC3339, str(ctx["timestamp"])); err != nil {
					t.Errorf("%s: timestamp: %v", action, err)
				}
				if !regexp.MustCompile(`^P(T\d+[HMS])+$`).MatchString(str(ctx["ttl"])) {
					t.Errorf("%s: ttl %q is not an ISO 8601 duration like PT30S", action, ctx["ttl"])
				}

				// 3. One order, one provider, and a quote that adds up.
				msg := lookup(doc, "message")
				for _, id := range []any{lookup(msg, "order", "id"), lookup(msg, "order_id")} {
					if id != nil {
						orderIDs[str(id)] = true
					}
				}
				if id := lookup(msg, "order", "provider", "id"); id != nil {
					providerIDs[str(id)] = true
				}
				if quote := lookup(msg, "order", "quote"); quote != nil {
					checkQuoteAddsUp(t, action, quote)
				}
			}
			if len(orderIDs) != 1 {
				t.Errorf("the journey must use one order id, got %v", sortedKeys(orderIDs))
			}
			if len(providerIDs) != 1 {
				t.Errorf("the journey must use one provider id, got %v", sortedKeys(providerIDs))
			}
		})
	}
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkQuoteAddsUp verifies quote.price.value equals the sum of the breakup prices (decimal arithmetic).
func checkQuoteAddsUp(t *testing.T, action string, quote any) {
	t.Helper()
	total, ok := new(big.Rat).SetString(str(lookup(quote, "price", "value")))
	if !ok {
		t.Errorf("%s: quote.price.value is not a number", action)
		return
	}
	sum := new(big.Rat)
	breakup, _ := lookup(quote, "breakup").([]any)
	for _, b := range breakup {
		v, ok := new(big.Rat).SetString(str(lookup(b, "price", "value")))
		if !ok {
			t.Errorf("%s: a breakup price is not a number", action)
			return
		}
		sum.Add(sum, v)
	}
	if total.Cmp(sum) != 0 {
		t.Errorf("%s: quote.price.value %s != sum of breakup %s", action, total.FloatString(2), sum.FloatString(2))
	}
}

// TestSchemasRejectBrokenFixtures guards the guard: a fixture with a required field removed or a value
// outside the ONDC rules must fail the same validation, otherwise TestFixturesAreONDCValid proves nothing.
func TestSchemasRejectBrokenFixtures(t *testing.T) {
	dir := extractedSchemas(t)
	validator, _, err := schemavalidator.New(context.Background(), &schemavalidator.Config{SchemaDir: dir})
	if err != nil {
		t.Fatalf("load schemas: %v", err)
	}
	for _, spec := range domains {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			body := readRendered(t, spec, "search")
			broken := mutate(t, body, func(d map[string]any) { delete(d["context"].(map[string]any), "ttl") })
			if err := validator.Validate(context.Background(), &url.URL{Path: "search"}, broken); err == nil {
				t.Error("a search without context.ttl must be rejected")
			}
			// A value outside the domain's rules.
			broken = mutate(t, body, func(d map[string]any) {
				intent := lookup(d, "message", "intent").(map[string]any)
				payment := intent["payment"].(map[string]any)
				if spec.name == "retail" {
					payment["@ondc/org/buyer_app_finder_fee_type"] = "flat" // ONDC retail allows only "percent"
				} else {
					payment["collected_by"] = "NOBODY" // TRV10 allows only BPP or BAP
				}
			})
			if err := validator.Validate(context.Background(), &url.URL{Path: "search"}, broken); err == nil {
				t.Error("a search with a value outside the ONDC rules must be rejected")
			}
		})
	}
}

// routingEndpoints returns the endpoints listed in a rendered routing template.
func routingEndpoints(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		RoutingRules []struct {
			Endpoints []string `yaml:"endpoints"`
		} `yaml:"routingRules"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var out []string
	for _, r := range cfg.RoutingRules {
		out = append(out, r.Endpoints...)
	}
	sort.Strings(out)
	return out
}

func listJSON(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	sort.Strings(out)
	return out
}

// TestEveryEndpointIsCovered keeps the three places that define "all Beckn endpoints" in step: the fixtures,
// the routing the tests configure and the schemas ONIX ships. A new endpoint added to one but not the others
// fails here, so no endpoint can be left untested by accident.
func TestEveryEndpointIsCovered(t *testing.T) {
	want := append([]string{}, allActions()...)
	sort.Strings(want)
	if len(want) != 20 {
		t.Fatalf("expected 20 endpoints (10 requests and 10 callbacks), have %d", len(want))
	}
	schemaRoot := extractedSchemas(t)
	for _, spec := range domains {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			if got := listJSON(t, filepath.Join("testdata", "fixtures", spec.name)); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("fixtures cover %v, want %v", got, want)
			}
			schemaFiles := listJSON(t, filepath.Join(schemaRoot, spec.schemaDir, spec.schemaVer))
			for _, a := range want {
				if !contains(schemaFiles, a) {
					t.Errorf("no %s schema in %s/%s", a, spec.schemaDir, spec.schemaVer)
				}
			}
			cfgDir := filepath.Join("testdata", "config", spec.name)
			requests := sortedCopy(requestActions)
			callbacks := sortedCopy(callbackNames())
			for file, wantEndpoints := range map[string][]string{
				"routing-bap-caller.yaml.tmpl":   requests,
				"routing-bpp-receiver.yaml.tmpl": requests,
				"routing-bpp-caller.yaml.tmpl":   callbacks,
				"routing-bap-receiver.yaml.tmpl": callbacks,
			} {
				if got := routingEndpoints(t, filepath.Join(cfgDir, file)); fmt.Sprint(got) != fmt.Sprint(wantEndpoints) {
					t.Errorf("%s routes %v, want %v", file, got, wantEndpoints)
				}
			}
		})
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func callbackNames() []string {
	var out []string
	for _, a := range requestActions {
		out = append(out, callbackOf(a))
	}
	return out
}
