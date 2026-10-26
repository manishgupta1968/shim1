package main

/*
To run this code and generate a new Ed25519 key pair, use the following command:
go run install/generate-ed25519-keys.go
*/

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log"
)

func main() {
	// Generate a new Ed25519 key pair using rand.Reader as default
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		log.Fatal("Failed to generate key pair:", err)
	}

	// The private key contains both seed and public key (64 bytes total)
	// We need to extract just the seed (first 32 bytes)
	seed := privateKey[:ed25519.SeedSize]

	fmt.Println("=== Ed25519 Key Pair ===")
	fmt.Printf("signingPrivateKey: %s\n", base64.StdEncoding.EncodeToString(seed))
	fmt.Printf("signingPublicKey: %s\n", base64.StdEncoding.EncodeToString(publicKey))

}
