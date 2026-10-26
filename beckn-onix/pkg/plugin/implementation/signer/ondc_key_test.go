package signer

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

// ONDC's own sample code (reference-implementations/utilities/signing_and_verification)
// stores a signing private key as base64 of the 64-byte ed25519 key (seed + public key).
// ONIX originally accepted only the 32-byte seed. Both forms must sign, and give
// the same signature, because the 64-byte form contains the same seed.
func TestSign_AcceptsONDC64ByteKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := New(context.Background(), &Config{})
	payload := []byte(`{"context":{"action":"search"}}`)

	fromSeed, err := s.Sign(context.Background(), payload, base64.StdEncoding.EncodeToString(priv.Seed()), 100, 200)
	if err != nil {
		t.Fatalf("32-byte seed: %v", err)
	}
	from64, err := s.Sign(context.Background(), payload, base64.StdEncoding.EncodeToString(priv), 100, 200)
	if err != nil {
		t.Fatalf("64-byte ONDC key must be accepted: %v", err)
	}
	if fromSeed != from64 {
		t.Error("the same key in both formats must give the same signature")
	}

	// The signature must verify with the public key, using the ONDC signing string.
	sig, _ := base64.StdEncoding.DecodeString(from64)
	signingString, _ := hash(payload, 100, 200)
	if !ed25519.Verify(pub, []byte(signingString), sig) {
		t.Error("signature does not verify")
	}
}

func TestSign_Rejects64ByteKeyWithWrongPublicHalf(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	broken := append(append([]byte{}, priv.Seed()...), otherPub...)

	s, _, _ := New(context.Background(), &Config{})
	_, err := s.Sign(context.Background(), []byte("x"), base64.StdEncoding.EncodeToString(broken), 1, 2)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("expected a 'does not match' error, got %v", err)
	}
}

func TestSign_RejectsOtherKeyLengths(t *testing.T) {
	s, _, _ := New(context.Background(), &Config{})
	for _, n := range []int{0, 16, 31, 33, 63, 65, 96} {
		key := base64.StdEncoding.EncodeToString(make([]byte, n))
		if _, err := s.Sign(context.Background(), []byte("x"), key, 1, 2); err == nil {
			t.Errorf("a %d-byte key must be rejected", n)
		}
	}
}
