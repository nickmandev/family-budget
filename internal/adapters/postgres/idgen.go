package postgres

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// UUIDGenerator is the production ports.IDGenerator. It emits canonical
// UUIDv4 strings from crypto/rand using only the standard library, avoiding a
// third-party UUID dependency.
type UUIDGenerator struct{}

// NewID returns a canonical lowercase UUIDv4 string.
func (UUIDGenerator) NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	// RFC 4122 version 4 and variant bits.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80

	var buf [36]byte
	hex.Encode(buf[0:8], raw[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], raw[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], raw[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], raw[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], raw[10:16])
	return string(buf[:]), nil
}
