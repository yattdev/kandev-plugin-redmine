// Package secretcrypto decodes deprecated v0.1 plugin-side envelopes solely
// for migration. It is not a security boundary: workspace IDs are identifiers,
// not secret key material. New API keys cross the plugin boundary only through
// the SDK's host-managed encrypted secret store; host-verified workspace
// context and dot-safe secret-key composition provide scoping.
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

// hkdfInfo/hkdfSalt identify only the legacy v0.1 envelope format. They do
// not make a workspace-ID-derived key confidential and must not be reused for
// new secret storage.
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
