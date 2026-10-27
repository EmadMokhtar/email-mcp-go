package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

// keySize is the size of every key in bytes. 32 bytes select AES-256.
const keySize = 32

var (
	// ErrDecrypt is returned by Open for every failure: wrong key, wrong
	// associated data, changed data, or data that is too short. It does not say
	// which one, so a caller cannot learn anything from the difference.
	ErrDecrypt = errors.New("secrets: decryption failed")

	// ErrKeySize is returned when a key is not exactly 32 bytes.
	ErrKeySize = errors.New("secrets: key must be exactly 32 bytes")
)

// AAD returns the associated data that Seal and Open bind to a value.
//
// The associated data is not stored in the sealed value. A value sealed for one
// tenant, account and field cannot be opened as another, even with the right
// key. This stops a copied database row from being used in another place.
//
// The ids must not contain "|". The format has no escaping, so ids that
// contain it could produce the same text for two different inputs.
func AAD(tenantID, accountID, field string) string {
	return "email-mcp/v1|tenant:" + tenantID + "|account:" + accountID + "|field:" + field
}

// newGCM returns an AES-256-GCM cipher for key.
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != keySize {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: create gcm: %w", err)
	}
	return gcm, nil
}

// Seal encrypts plaintext with AES-256-GCM. The key must be 32 bytes.
//
// The aad (see AAD) is authenticated but not stored. Open needs the same aad.
// Every call uses a new random 96-bit nonce. The result is nonce || ciphertext,
// where the ciphertext ends with the 16-byte authentication tag.
func Seal(key, plaintext []byte, aad string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: create nonce: %w", err)
	}
	// Seal appends the ciphertext to its first argument, so the nonce stays in
	// front of it.
	return gcm.Seal(nonce, nonce, plaintext, []byte(aad)), nil
}

// Open decrypts a value made by Seal. It needs the same key and aad.
//
// Every failure returns ErrDecrypt. A key that is not 32 bytes is a mistake in
// the calling code, not a property of the data, so that error also matches
// ErrKeySize.
func Open(key, sealed []byte, aad string) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecrypt, err)
	}
	nonceSize := gcm.NonceSize()
	if len(sealed) < nonceSize+gcm.Overhead() {
		return nil, ErrDecrypt
	}
	nonce, ciphertext := sealed[:nonceSize], sealed[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		// Drop the details of err on purpose. See ErrDecrypt.
		return nil, ErrDecrypt
	}
	return plaintext, nil
}
