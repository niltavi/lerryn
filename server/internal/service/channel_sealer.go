package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/store"
)

// KeyringSealer encrypts notification channel secrets with the credential
// envelope encryption (tech design 12.3): a data key per value, wrapped by
// the master key. Rows written by PlainSealer before encryption was
// available still open, and are sealed properly on their next update.
type KeyringSealer struct {
	Keys secrets.Keyring
}

var _ ChannelSealer = KeyringSealer{}

// channelAAD binds sealed values to their purpose, so a channel secret
// cannot be presented as a credential version or the other way round.
var channelAAD = []byte("lerryn/notification-channel/v1")

// Seal implements ChannelSealer.
func (k KeyringSealer) Seal(_ context.Context, plaintext []byte) (SealedSecret, error) {
	if k.Keys == nil {
		return SealedSecret{}, errors.New("channel sealer: no master key configured")
	}
	s, err := secrets.Seal(k.Keys, plaintext, channelAAD)
	if err != nil {
		return SealedSecret{}, err
	}
	return SealedSecret{Ciphertext: s.Ciphertext, WrappedDEK: s.WrappedDEK, KEKID: s.KEKID}, nil
}

// Open implements ChannelSealer.
func (k KeyringSealer) Open(ctx context.Context, s SealedSecret) ([]byte, error) {
	if s.KEKID == PlainKEKID {
		return PlainSealer{}.Open(ctx, s)
	}
	if k.Keys == nil {
		return nil, errors.New("channel sealer: no master key configured")
	}
	return secrets.Open(k.Keys, secrets.Sealed{Ciphertext: s.Ciphertext, WrappedDEK: s.WrappedDEK, KEKID: s.KEKID}, channelAAD)
}

// RewrapChannelSecrets moves every notification channel secret onto the
// keyring's current master key after a rotation, and seals rows still
// stored by PlainSealer. It returns how many rows it changed.
func RewrapChannelSecrets(ctx context.Context, db *store.DB, keys secrets.Keyring) (int, error) {
	if keys == nil {
		return 0, errors.New("channel sealer: no master key configured")
	}
	sealer := KeyringSealer{Keys: keys}
	var rows []store.NotificationChannel
	if err := db.NewSelect().Model(&rows).Column("id", "secret_ciphertext", "secret_wrapped_dek", "secret_kek_id").
		Where("secret_kek_id <> ?", keys.CurrentID()).Scan(ctx); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		old := SealedSecret{Ciphertext: r.SecretCiphertext, WrappedDEK: r.SecretWrappedDEK, KEKID: r.SecretKEKID}
		plain, err := sealer.Open(ctx, old)
		if err != nil {
			return n, fmt.Errorf("open the secret of channel %s: %w", r.ID, err)
		}
		sealed, err := sealer.Seal(ctx, plain)
		clear(plain)
		if err != nil {
			return n, err
		}
		if _, err := db.NewUpdate().Model((*store.NotificationChannel)(nil)).
			Set("secret_ciphertext = ?", sealed.Ciphertext).
			Set("secret_wrapped_dek = ?", sealed.WrappedDEK).
			Set("secret_kek_id = ?", sealed.KEKID).
			Where("id = ? AND secret_kek_id = ?", r.ID, r.SecretKEKID).Exec(ctx); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
