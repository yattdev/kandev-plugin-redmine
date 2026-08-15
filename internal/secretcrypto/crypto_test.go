package secretcrypto_test

import (
	"testing"

	"kandev-plugin-redmine/internal/secretcrypto"

	"github.com/stretchr/testify/require"
)

func TestEncryptDecrypt_RoundTrips(t *testing.T) {
	encrypted, err := secretcrypto.Encrypt("ws-1", "s3cret-api-key")
	require.NoError(t, err)
	require.NotEmpty(t, encrypted)
	require.NotContains(t, encrypted, "s3cret-api-key")

	plaintext, err := secretcrypto.Decrypt("ws-1", encrypted)
	require.NoError(t, err)
	require.Equal(t, "s3cret-api-key", plaintext)
}

func TestDecrypt_WrongWorkspaceID_Fails(t *testing.T) {
	encrypted, err := secretcrypto.Encrypt("ws-1", "s3cret-api-key")
	require.NoError(t, err)

	_, err = secretcrypto.Decrypt("ws-2", encrypted)
	require.Error(t, err)
}

func TestEncrypt_DifferentWorkspaces_ProduceDifferentCiphertext(t *testing.T) {
	a, err := secretcrypto.Encrypt("ws-1", "same-plaintext")
	require.NoError(t, err)
	b, err := secretcrypto.Encrypt("ws-2", "same-plaintext")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

func TestEncrypt_SamePlaintextTwice_ProducesDifferentCiphertext(t *testing.T) {
	// Random nonce per call: two encryptions of the same plaintext under the
	// same workspace must not be comparable/identical ciphertext.
	a, err := secretcrypto.Encrypt("ws-1", "same-plaintext")
	require.NoError(t, err)
	b, err := secretcrypto.Encrypt("ws-1", "same-plaintext")
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

func TestEncrypt_EmptyWorkspaceID_Errors(t *testing.T) {
	_, err := secretcrypto.Encrypt("", "s3cret")
	require.Error(t, err)
}

func TestDecrypt_TamperedCiphertext_Fails(t *testing.T) {
	encrypted, err := secretcrypto.Encrypt("ws-1", "s3cret-api-key")
	require.NoError(t, err)

	tampered := []byte(encrypted)
	tampered[len(tampered)-1] ^= 0x01
	_, err = secretcrypto.Decrypt("ws-1", string(tampered))
	require.Error(t, err)
}
