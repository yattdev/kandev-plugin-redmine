// Package secretcrypto reads the deprecated v0.1 plugin-side envelope solely
// for migration. New API keys go directly to the host's SetSecret RPC. The host's
// plugin secret store is namespaced only by plugin ID
// (plugin:<id>:secret:<key>), not by workspace, so internal/connection
// composes the workspace ID into the secret *key* itself
// (see internal/connection.secretKey) — that alone is a naming convention,
// not isolation. This package adds the actual isolation: decrypting under
// the wrong workspace ID fails closed (AEAD authentication failure) instead
// of silently returning another workspace's key, converting a
// key-composition bug elsewhere in the plugin from a credential leak into a
// loud error (see docs/plans/redmine-plugin/plan.md "Risks").
package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// hkdfInfo/hkdfSalt are fixed, non-secret domain-separation constants. They
// do not by themselves make the derived key secret — that comes from the
// workspace ID being an unguessable UUID plus the host's own encryption of
// SetSecret values at rest (internal/plugins/host.go). This layer's job is
// isolation, not primary confidentiality: see the package doc comment.
const hkdfInfo = "kandev-plugin-redmine/api-key/v1"

var hkdfSalt = []byte("kandev-plugin-redmine/workspace-secret-salt/v1")

func deriveKey(workspaceID string) ([]byte, error) {
	if workspaceID == "" {
		return nil, errors.New("secretcrypto: workspace id is required")
	}
	key := make([]byte, 32)
	kdf := hkdf.New(sha256.New, []byte(workspaceID), hkdfSalt, []byte(hkdfInfo))
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, fmt.Errorf("secretcrypto: deriving key: %w", err)
	}
	return key, nil
}

func newGCM(workspaceID string) (cipher.AEAD, error) {
	key, err := deriveKey(workspaceID)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretcrypto: creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretcrypto: creating gcm: %w", err)
	}
	return gcm, nil
}

// Encrypt returns plaintext encrypted under a key derived from workspaceID.
// Deprecated: only legacy migration tests should call this.
// base64-encoded (nonce prepended). Two calls with identical inputs produce
// different output (random nonce per call).
func Encrypt(workspaceID, plaintext string) (string, error) {
	gcm, err := newGCM(workspaceID)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secretcrypto: generating nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses the legacy Encrypt format. It fails if workspaceID does not match the one
// Encrypt was called with, or if encoded was tampered with.
func Decrypt(workspaceID, encoded string) (string, error) {
	gcm, err := newGCM(workspaceID)
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("secretcrypto: decoding ciphertext: %w", err)
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("secretcrypto: ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("secretcrypto: decrypting: %w", err)
	}
	return string(plaintext), nil
}
