package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/ondcregistry"
	"github.com/beckn-one/beckn-onix/pkg/plugin/implementation/signer"
)

// Identities used by the demo. Ports match config/shim1/routing-*.yaml.
const (
	sellerID    = "seller.shim1.local"
	sellerKeyID = "seller-key-1"
	sellerURI   = "http://localhost:8081/bpp/receiver"
	buyerURI    = "http://localhost:9002/bap"
	domain      = "ONDC:RET10"
)

// participant is one demo identity with its key pair (base64; the private key is the ed25519 seed).
type participant struct {
	ID      string `json:"id"`
	KeyID   string `json:"keyId"`
	Private string `json:"private"`
	Public  string `json:"public"`
}

func newParticipant(id, keyID string) (participant, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return participant{}, err
	}
	return participant{
		ID:      id,
		KeyID:   keyID,
		Private: base64.StdEncoding.EncodeToString(priv.Seed()),
		Public:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// authHeader builds an ONDC Authorization (or X-Gateway-Authorization) header
// value signing body, using the same signer and header layout as ONIX.
func (p participant) authHeader(body []byte, created, expires int64) (string, error) {
	s, _, err := signer.New(context.Background(), &signer.Config{})
	if err != nil {
		return "", err
	}
	sig, err := s.Sign(context.Background(), body, p.Private, created, expires)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`Signature keyId="%s|%s|ed25519",algorithm="ed25519",created="%d",expires="%d",headers="(created) (expires) digest",signature="%s"`,
		p.ID, p.KeyID, created, expires, sig), nil
}

// demoKeys is the content of keys.json. The client reads it to sign requests.
type demoKeys struct {
	Seller    participant `json:"seller"`    // our own seller app (BPP), the one Shim 1 acts for
	Buyer     participant `json:"buyer"`     // a good buyer app (BAP)
	Gateway   participant `json:"gateway"`   // a good gateway (BG)
	Pending   participant `json:"pending"`   // BAP whose registration is not finished (status INITIATED)
	Lapsed    participant `json:"lapsed"`    // BAP whose registration has expired (valid_until in the past)
	WrongType participant `json:"wrongType"` // registered, but as a seller (BPP), not a buyer
	Stranger  participant `json:"stranger"`  // not in the registry at all
}

func runKeys(args []string) error {
	fs := flag.NewFlagSet("keys", flag.ExitOnError)
	out := fs.String("out", "config/shim1/generated", "output folder")
	tmplPath := fs.String("template", "config/shim1/shim1.yaml.tmpl", "Shim 1 config template")
	fs.Parse(args)

	var k demoKeys
	for _, e := range []struct {
		dst       *participant
		id, keyID string
	}{
		{&k.Seller, sellerID, sellerKeyID},
		{&k.Buyer, "buyer.shim1.local", "buyer-key-1"},
		{&k.Gateway, "gateway.shim1.local", "gateway-key-1"},
		{&k.Pending, "pending.shim1.local", "pending-key-1"},
		{&k.Lapsed, "lapsed.shim1.local", "lapsed-key-1"},
		{&k.WrongType, "otherseller.shim1.local", "otherseller-key-1"},
		{&k.Stranger, "stranger.shim1.local", "stranger-key-1"},
	} {
		p, err := newParticipant(e.id, e.keyID)
		if err != nil {
			return err
		}
		*e.dst = p
	}

	now := time.Now().UTC()
	entry := func(p participant, typ, status string, from, until time.Time) ondcregistry.Entry {
		return ondcregistry.Entry{
			SubscriberID: p.ID, UkID: p.KeyID, SubscriberURL: "http://" + p.ID,
			Type: typ, Domain: domain, City: "*", Country: "IND",
			SigningPublicKey: p.Public, EncrPublicKey: p.Public,
			Status: status, ValidFrom: from, ValidUntil: until,
		}
	}
	from, until := now.Add(-24*time.Hour), now.Add(365*24*time.Hour)
	// The stranger is deliberately left out.
	registry := []ondcregistry.Entry{
		entry(k.Seller, ondcregistry.TypeBPP, "SUBSCRIBED", from, until),
		entry(k.Buyer, ondcregistry.TypeBAP, "SUBSCRIBED", from, until),
		entry(k.Gateway, ondcregistry.TypeBG, "SUBSCRIBED", from, until),
		entry(k.Pending, ondcregistry.TypeBAP, "INITIATED", from, until),
		entry(k.Lapsed, ondcregistry.TypeBAP, "SUBSCRIBED", from.Add(-30*24*time.Hour), now.Add(-time.Hour)),
		entry(k.WrongType, ondcregistry.TypeBPP, "SUBSCRIBED", from, until),
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(*out, "keys.json"), k, 0o600); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(*out, "registry.json"), registry, 0o644); err != nil {
		return err
	}

	tmpl, err := template.ParseFiles(*tmplPath)
	if err != nil {
		return err
	}
	cfg, err := os.OpenFile(filepath.Join(*out, "shim1.yaml"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer cfg.Close()
	if err := tmpl.Execute(cfg, map[string]string{
		"SellerID": sellerID, "SellerKeyID": sellerKeyID, "SellerURI": sellerURI,
		"SellerPrivate": k.Seller.Private, "SellerPublic": k.Seller.Public,
	}); err != nil {
		return err
	}
	fmt.Printf("wrote keys.json, registry.json and shim1.yaml to %s\n", *out)
	return nil
}

func writeJSON(path string, v any, perm os.FileMode) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), perm)
}
