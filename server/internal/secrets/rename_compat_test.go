package secrets

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// Captured with the pre-rename implementation using a synthetic master key.
// Round trips alone would miss simultaneous changes to sealing and opening.
func TestPreRenameCredentialCompatibility(t *testing.T) {
	kr := testKeyring(t, bytes.Repeat([]byte{0x42}, KeySize))
	const keyID = "local-c9b2d8de6477d176"
	if kr.CurrentID() != keyID {
		t.Fatalf("master key ID = %q, want %q", kr.CurrentID(), keyID)
	}
	decode := func(s string) []byte {
		t.Helper()
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	sealed := Sealed{
		KEKID:      keyID,
		Ciphertext: decode("7d342c827a24c2aa227578b9001e86d937aba54fbcb9e9905b7fab9cbb13bb5dfec1fca48859894a3c9a1fb084a1580c1b9ae1"),
		WrappedDEK: decode("1c7e97c226d3e29efae807df2fce269dc132255b08d06059ddc1b4b680b07874707a95a819f9a5f1550c0ee62bfd742f4612754727d90c0a49d4273f"),
	}
	aad := []byte("credential/rename-test/v1")
	plain, err := Open(kr, sealed, aad)
	if err != nil || string(plain) != "synthetic-legacy-secret" {
		t.Fatalf("pre-rename credential did not decrypt: %v", err)
	}
	const derived = "c736b7268cc6debca177c33514ec98765bd8a7ef11fc598804a8d1a07dd08305"
	if got := hex.EncodeToString(kr.DeriveKey("rename-test")); got != derived {
		t.Fatal("pre-rename derived key changed")
	}
	rotated := testKeyring(t, bytes.Repeat([]byte{0x43}, KeySize), bytes.Repeat([]byte{0x42}, KeySize))
	moved, err := Rewrap(rotated, sealed, aad)
	if err != nil {
		t.Fatal(err)
	}
	plain, err = Open(rotated, moved, aad)
	if err != nil || string(plain) != "synthetic-legacy-secret" {
		t.Fatalf("pre-rename credential did not survive key rotation: %v", err)
	}
}
