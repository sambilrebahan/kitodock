// Package credentials seals short secrets for the Windows account running
// the app, using DPAPI (CryptProtectData). A sealed value only opens for the
// same user on the same machine, which is the right property for things like
// saved SSH passwords: at rest they are opaque, and carrying an environment
// folder to another account simply means re-entering them.
//
// Sealed values carry the "kddp1:" prefix followed by the base64 DPAPI blob,
// so on-disk formats can evolve with a new prefix rather than a migration.
package credentials

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const sealPrefix = "kddp1:"

// Sealer is the interface other packages depend on; System is the production
// implementation and tests substitute their own.
type Sealer interface {
	Seal(plain string) (string, error)
	Unseal(sealed string) (string, error)
}

// System seals with the current Windows account's DPAPI key.
type System struct{}

var _ Sealer = System{}

func (System) Seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	blob, err := protect([]byte(plain))
	if err != nil {
		return "", fmt.Errorf("seal secret for this Windows account: %w", err)
	}
	return sealPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

func (System) Unseal(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, sealPrefix) {
		return "", errors.New("not a sealed secret (missing prefix)")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil || len(raw) == 0 {
		return "", errors.New("sealed secret is malformed")
	}
	plain, err := unprotect(raw)
	if err != nil {
		return "", fmt.Errorf("open secret for this Windows account: %w", err)
	}
	defer clear(plain)
	return string(plain), nil
}
