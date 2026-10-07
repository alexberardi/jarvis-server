// Command testsign makes a throwaway minisign key and signs fake releases with it, for CI jobs
// that run on runners without the minisign tool (and must not put it on PATH: the install
// scripts then require the project's signature). Never used for real releases.
//
//	go run ./scripts/testsign keygen KEYFILE           # writes the seed, prints the public key
//	go run ./scripts/testsign sign KEYFILE FILE COMMENT # writes FILE.minisig (prehashed "ED")
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// keyID is fixed: the key is thrown away with the job.
var keyID = [8]byte{0x74, 0x65, 0x73, 0x74, 0x73, 0x69, 0x67, 0x6e}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "testsign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "keygen":
		seed := make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return err
		}
		if err := os.WriteFile(args[1], []byte(hex.EncodeToString(seed)), 0o600); err != nil {
			return err
		}
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		fmt.Println(base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), keyID[:]...), pub...)))
		return nil
	case len(args) == 4 && args[0] == "sign":
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		seed, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return fmt.Errorf("%s is not a testsign key", args[1])
		}
		msg, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		priv := ed25519.NewKeyFromSeed(seed)
		h := blake2b.Sum512(msg)
		sig := ed25519.Sign(priv, h[:])
		global := ed25519.Sign(priv, append(bytes.Clone(sig), args[3]...))
		var b bytes.Buffer
		b.WriteString("untrusted comment: testsign throwaway key\n")
		b.WriteString(base64.StdEncoding.EncodeToString(append(append([]byte("ED"), keyID[:]...), sig...)) + "\n")
		b.WriteString("trusted comment: " + args[3] + "\n")
		b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
		return os.WriteFile(args[2]+".minisig", b.Bytes(), 0o644)
	}
	return fmt.Errorf("usage: testsign keygen KEYFILE | testsign sign KEYFILE FILE COMMENT")
}
