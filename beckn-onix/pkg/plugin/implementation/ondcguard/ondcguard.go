// Package ondcguard is an ONIX pipeline step with the ONDC checks that ONIX does
// not have. It runs after validateSign (so the signatures are already proven
// genuine) and does four things:
//
//  1. For gateway actions (search by default) the X-Gateway-Authorization header
//     must be present, and its signer must be registered as type BG.
//  2. The signer of the Authorization header must be registered as type BAP for
//     the request's domain and city.
//  3. The request must not be stale: context.timestamp must be within
//     context.ttl (an ISO 8601 duration such as PT30S) of now.
//  4. The request must not be a replay: the same bap_id + transaction_id +
//     message_id + action is accepted only once while it is still fresh.
//
// Failures are returned as model.CodedErr values; the ONDC NACK writer in
// core/module/handler turns them into ONDC error codes (30016, 30022, 30000).
package ondcguard

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/log"
	"github.com/beckn-one/beckn-onix/pkg/model"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcregistry"
)

// Config configures the guard.
type Config struct {
	Registry       ondcregistry.Config
	GatewayActions []string      // actions that must carry a gateway signature (default: search)
	FutureSkew     time.Duration // how far in the future a timestamp may be (default 30s)
	DefaultTTL     time.Duration // used when a request has no usable ttl (default 5m)
}

// registry is the part of ondcregistry.Client the guard needs.
type registry interface {
	Find(ctx context.Context, q ondcregistry.Query) ([]ondcregistry.Entry, error)
}

// Guard is the ondcguard step.
type Guard struct {
	cfg     Config
	reg     registry
	gateway map[string]bool
	now     func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time // replay key -> time until which it is remembered
}

// New builds a Guard and its registry client.
func New(ctx context.Context, cfg Config) (*Guard, error) {
	reg, err := ondcregistry.New(ctx, cfg.Registry)
	if err != nil {
		return nil, err
	}
	return newGuard(cfg, reg), nil
}

func newGuard(cfg Config, reg registry) *Guard {
	if len(cfg.GatewayActions) == 0 {
		cfg.GatewayActions = []string{"search"}
	}
	if cfg.FutureSkew == 0 {
		cfg.FutureSkew = 30 * time.Second
	}
	if cfg.DefaultTTL == 0 {
		cfg.DefaultTTL = 5 * time.Minute
	}
	g := &Guard{
		cfg:     cfg,
		reg:     reg,
		gateway: map[string]bool{},
		now:     time.Now,
		seen:    map[string]time.Time{},
	}
	for _, a := range cfg.GatewayActions {
		g.gateway[a] = true
	}
	return g
}

// Run implements definition.Step.
func (g *Guard) Run(ctx *model.StepContext) error {
	_, c, becknErr := model.ExtractContext(ctx.Body)
	if becknErr != nil {
		return model.NewBadReqErr(model.ONDCCodeInvalidRequest, fmt.Errorf("unreadable context: %s", becknErr.Message))
	}
	str := func(k string) string { s, _ := c[k].(string); return s }
	action, domain, city := str("action"), str("domain"), str("city")

	if err := g.checkSenders(ctx, action, domain, city); err != nil {
		return err
	}
	ttl, err := g.checkFresh(str("timestamp"), str("ttl"))
	if err != nil {
		return err
	}
	return g.checkReplay(strings.Join([]string{str("bap_id"), str("transaction_id"), str("message_id"), action}, "|"), ttl)
}

// checkSenders verifies who signed the request and in what role.
func (g *Guard) checkSenders(ctx *model.StepContext, action, domain, city string) error {
	hdr := ctx.Request.Header

	if g.gateway[action] {
		gw := hdr.Get(model.AuthHeaderGateway)
		if gw == "" {
			ctx.RespHeader.Set(model.UnaAuthorizedHeaderGateway, fmt.Sprintf("Signature realm=%q", ctx.SubID))
			return model.NewSignValidationErr("AUT_SIGNATURE_MISSING",
				fmt.Errorf("%s is required for %s", model.AuthHeaderGateway, action))
		}
		if err := g.requireType(ctx, gw, ondcregistry.TypeBG, domain, city); err != nil {
			return err
		}
	}
	return g.requireType(ctx, hdr.Get(model.AuthHeaderSubscriber), ondcregistry.TypeBAP, domain, city)
}

// requireType checks that the signer named in an Authorization-style header is
// registered with the wanted type for the domain and city.
func (g *Guard) requireType(ctx *model.StepContext, header, wantType, domain, city string) error {
	sub, ukID, ok := parseKeyID(header)
	if !ok {
		return model.NewSignValidationErr("AUT_SIGNATURE_INVALID", fmt.Errorf("cannot read keyId from header"))
	}
	found, err := g.reg.Find(ctx, ondcregistry.Query{SubscriberID: sub, UkID: ukID, Type: wantType, Domain: domain, City: city})
	if err != nil {
		return fmt.Errorf("ondcguard: registry lookup failed: %w", err)
	}
	if len(found) == 0 {
		log.Warnf(ctx, "ondcguard: %s is not a %s for domain=%s city=%s", sub, wantType, domain, city)
		return model.NewSignValidationErr("AUT_UNAUTHORIZED_ACTION",
			fmt.Errorf("%s is not registered as %s for domain %s and city %s", sub, wantType, domain, city))
	}
	return nil
}

// checkFresh rejects a request whose timestamp is outside its ttl window. It
// returns the ttl it used, so the replay window can match it.
func (g *Guard) checkFresh(timestamp, ttlText string) (time.Duration, error) {
	ts, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return 0, model.NewBadReqErr(model.ONDCCodeInvalidRequest, fmt.Errorf("context.timestamp %q is not an RFC 3339 time", timestamp))
	}
	ttl := g.cfg.DefaultTTL
	if ttlText != "" {
		if ttl, err = ParseISODuration(ttlText); err != nil {
			return 0, model.NewBadReqErr(model.ONDCCodeInvalidRequest, fmt.Errorf("context.ttl %q: %v", ttlText, err))
		}
	}
	now := g.now()
	if now.Sub(ts) > ttl {
		return 0, model.NewBadReqErr(model.ONDCCodeStaleRequest,
			fmt.Errorf("stale request: timestamp %s is older than ttl %s", timestamp, ttlText))
	}
	if ts.Sub(now) > g.cfg.FutureSkew {
		return 0, model.NewBadReqErr(model.ONDCCodeStaleRequest,
			fmt.Errorf("timestamp %s is in the future", timestamp))
	}
	return ttl, nil
}

// checkReplay remembers key for ttl (plus the allowed skew) and rejects a second
// arrival of the same key. In-memory only: it is lost on restart and not shared
// between instances. Use a shared store (for example Redis) in production.
func (g *Guard) checkReplay(key string, ttl time.Duration) error {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, until := range g.seen {
		if !now.Before(until) {
			delete(g.seen, k)
		}
	}
	if _, dup := g.seen[key]; dup {
		return model.NewBadReqErr(model.ONDCCodeInvalidRequest, fmt.Errorf("duplicate request: message_id already processed"))
	}
	g.seen[key] = now.Add(ttl + 2*g.cfg.FutureSkew)
	return nil
}

// parseKeyID reads subscriber id and unique key id from the keyId="sub|ukId|alg"
// part of an Authorization or X-Gateway-Authorization header.
func parseKeyID(header string) (subscriber, ukID string, ok bool) {
	const marker = `keyId="`
	i := strings.Index(header, marker)
	if i < 0 {
		return "", "", false
	}
	rest := header[i+len(marker):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return "", "", false
	}
	parts := strings.Split(rest[:end], "|")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

var isoDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// ParseISODuration parses the ISO 8601 durations ONDC uses for ttl, such as
// PT30S, PT5M, PT1H30M or P1D. Years, months and weeks are not supported.
func ParseISODuration(s string) (time.Duration, error) {
	m := isoDuration.FindStringSubmatch(s)
	if m == nil || s == "P" || strings.HasSuffix(s, "T") {
		return 0, fmt.Errorf("not an ISO 8601 duration such as PT30S")
	}
	num := func(v string) float64 {
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	secs := num(m[1])*86400 + num(m[2])*3600 + num(m[3])*60 + num(m[4])
	return time.Duration(secs * float64(time.Second)), nil
}

// ensure the registry client satisfies the guard's needs.
var _ registry = (*ondcregistry.Client)(nil)
