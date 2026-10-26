package ondc_e2e_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/blake2b"
)

// This file signs and verifies ONDC request signatures on its own, without any ONIX code. It follows the
// ONDC/Beckn signing rules, so the tests prove that ONIX speaks the ONDC wire format, not just that two ONIX
// processes understand each other:
//
//	digest         = "BLAKE-512=" + base64(blake2b-512(body))
//	signing string = "(created): <unix>\n(expires): <unix>\ndigest: <digest>"
//	signature      = base64(ed25519(signing string))
//	header         = Signature keyId="<subscriber_id>|<unique_key_id>|ed25519",algorithm="ed25519",
//	                 created="<unix>",expires="<unix>",headers="(created) (expires) digest",signature="<sig>"

func digestOf(body []byte) string {
	h := blake2b.Sum512(body)
	return "BLAKE-512=" + base64.StdEncoding.EncodeToString(h[:])
}

func signingString(body []byte, created, expires int64) string {
	return fmt.Sprintf("(created): %d\n(expires): %d\ndigest: %s", created, expires, digestOf(body))
}

// signBody returns the Authorization header value for body, signed by p.
func signBody(p participant, body []byte, created, expires time.Time) string {
	c, e := created.Unix(), expires.Unix()
	sig := ed25519.Sign(p.Priv, []byte(signingString(body, c, e)))
	return fmt.Sprintf(
		`Signature keyId="%s|%s|ed25519",algorithm="ed25519",created="%d",expires="%d",headers="(created) (expires) digest",signature="%s"`,
		p.ID, p.KeyID, c, e, base64.StdEncoding.EncodeToString(sig))
}

// authHeader is a parsed Authorization / X-Gateway-Authorization header.
type authHeader struct {
	SubscriberID string
	KeyID        string
	Algorithm    string
	Created      int64
	Expires      int64
	Headers      string
	Signature    []byte
}

func parseAuth(header string) (authHeader, error) {
	var a authHeader
	rest, ok := strings.CutPrefix(header, "Signature ")
	if !ok {
		return a, fmt.Errorf("header does not start with %q", "Signature ")
	}
	attrs := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return a, fmt.Errorf("malformed attribute %q", part)
		}
		attrs[k] = strings.Trim(v, `"`)
	}
	ids := strings.Split(attrs["keyId"], "|")
	if len(ids) != 3 {
		return a, fmt.Errorf("keyId %q is not <subscriber_id>|<unique_key_id>|<algorithm>", attrs["keyId"])
	}
	a.SubscriberID, a.KeyID, a.Algorithm = ids[0], ids[1], ids[2]
	var err error
	if a.Created, err = strconv.ParseInt(attrs["created"], 10, 64); err != nil {
		return a, fmt.Errorf("created: %w", err)
	}
	if a.Expires, err = strconv.ParseInt(attrs["expires"], 10, 64); err != nil {
		return a, fmt.Errorf("expires: %w", err)
	}
	a.Headers = attrs["headers"]
	if a.Signature, err = base64.StdEncoding.DecodeString(attrs["signature"]); err != nil {
		return a, fmt.Errorf("signature: %w", err)
	}
	if attrs["algorithm"] != "ed25519" {
		return a, fmt.Errorf("algorithm %q, want ed25519", attrs["algorithm"])
	}
	return a, nil
}

// verifyAuth checks header against body and the signer's public key: format, validity window and signature.
func verifyAuth(header string, body []byte, signer participant, now time.Time) (authHeader, error) {
	a, err := parseAuth(header)
	if err != nil {
		return a, err
	}
	if a.Algorithm != "ed25519" {
		return a, fmt.Errorf("keyId algorithm %q, want ed25519", a.Algorithm)
	}
	if a.Headers != "(created) (expires) digest" {
		return a, fmt.Errorf("headers %q, want %q", a.Headers, "(created) (expires) digest")
	}
	if a.SubscriberID != signer.ID || a.KeyID != signer.KeyID {
		return a, fmt.Errorf("keyId is %s|%s, want %s|%s", a.SubscriberID, a.KeyID, signer.ID, signer.KeyID)
	}
	if now.Unix() < a.Created-5 || now.Unix() > a.Expires {
		return a, fmt.Errorf("signature is outside its validity window (created %d, expires %d, now %d)", a.Created, a.Expires, now.Unix())
	}
	if !ed25519.Verify(signer.Pub, []byte(signingString(body, a.Created, a.Expires)), a.Signature) {
		return a, fmt.Errorf("signature does not verify against the body")
	}
	return a, nil
}
