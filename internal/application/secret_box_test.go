package application

import (
	"encoding/base64"
	"testing"
)

func TestSecretBoxRoundTrip(t *testing.T) {
	box, err := NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewSecretBox: %v", err)
	}
	sealed, err := box.Seal("provider-session-id")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != "provider-session-id" {
		t.Fatalf("Open = %q, want original plaintext", opened)
	}
}

func TestSecretBoxRejectsTamperedCiphertext(t *testing.T) {
	box, err := NewSecretBox(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewSecretBox: %v", err)
	}
	sealed, err := box.Seal("secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	data, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	data[len(data)-1] ^= 1
	if _, err := box.Open(base64.StdEncoding.EncodeToString(data)); err == nil {
		t.Fatal("Open accepted tampered ciphertext")
	}
}

func TestNewSecretBoxValidatesKeyLength(t *testing.T) {
	for _, length := range []int{0, 16, 31, 33} {
		if _, err := NewSecretBox(make([]byte, length)); err == nil {
			t.Errorf("NewSecretBox accepted %d-byte key", length)
		}
	}
}
