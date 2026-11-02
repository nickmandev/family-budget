package application

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

// SecretBox encrypts short provider credentials for storage at rest.
type SecretBox struct {
	aead cipher.AEAD
}

// NewSecretBox constructs an AES-256-GCM secret box from a 32-byte key.
func NewSecretBox(key []byte) (*SecretBox, error) {
	if len(key) != 32 {
		return nil, errors.New("application: secret box key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("application: unable to initialize secret box")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("application: unable to initialize secret box")
	}
	return &SecretBox{aead: aead}, nil
}

// Seal encrypts plaintext and returns base64(nonce || ciphertext).
func (b *SecretBox) Seal(plaintext string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", errors.New("application: unable to generate encryption nonce")
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value produced by Seal.
func (b *SecretBox) Open(encoded string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(sealed) < b.aead.NonceSize()+b.aead.Overhead() {
		return "", errors.New("application: encrypted value is invalid")
	}
	nonce := sealed[:b.aead.NonceSize()]
	plaintext, err := b.aead.Open(nil, nonce, sealed[b.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("application: encrypted value authentication failed")
	}
	return string(plaintext), nil
}
