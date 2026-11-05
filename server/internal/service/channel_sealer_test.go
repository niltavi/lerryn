package service_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/service"
)

func TestKeyringSealer(t *testing.T) {
	ctx := context.Background()
	key, _ := secrets.GenerateKey()
	kr, _ := secrets.NewLocalKeyring(key)
	s := service.KeyringSealer{Keys: kr}
	target := []byte("https://hooks.example.com/T000/B000/XXXX")
	sealed, err := s.Seal(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.KEKID != kr.CurrentID() || bytes.Contains(sealed.Ciphertext, target) || len(sealed.WrappedDEK) == 0 {
		t.Fatalf("sealed = %+v", sealed)
	}
	if got, err := s.Open(ctx, sealed); err != nil || !bytes.Equal(got, target) {
		t.Fatalf("open = %q, %v", got, err)
	}
	// Rows from before encryption still open.
	plain, _ := service.PlainSealer{}.Seal(ctx, target)
	if got, err := s.Open(ctx, plain); err != nil || !bytes.Equal(got, target) {
		t.Fatalf("open plain = %q, %v", got, err)
	}
	// A tampered row fails instead of decrypting to something else.
	sealed.Ciphertext[len(sealed.Ciphertext)-1] ^= 1
	if _, err := s.Open(ctx, sealed); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("open tampered = %v", err)
	}
	// A credential ciphertext cannot be opened as a channel secret.
	cred, _ := secrets.Seal(kr, target, []byte("lerryn/credential/x/v1"))
	if _, err := s.Open(ctx, service.SealedSecret{Ciphertext: cred.Ciphertext, WrappedDEK: cred.WrappedDEK, KEKID: cred.KEKID}); !errors.Is(err, secrets.ErrDecrypt) {
		t.Fatalf("open a credential as a channel secret = %v", err)
	}
}
