package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	// argon2Version is PHC's Argon2 version 19. It is encoded in the hash so a
	// future version change can still verify old hashes.
	argon2Version = 19

	minPasswordBytes = 8
	maxPasswordBytes = 1024

	// maxEncodedHashLength bounds parsing work for a corrupted/oversized hash.
	maxEncodedHashLength = 512
)

// Argon2Params are the Argon2id cost parameters. They are configurable so a
// deployment can re-benchmark on its hardware (PLATFORM_DECISIONS §B); the
// values used to create a hash are encoded in the hash string, so changing the
// configuration does not invalidate existing passwords.
type Argon2Params struct {
	Time       uint32 // passes over memory
	MemoryKiB  uint32 // memory cost (KiB)
	Threads    uint8  // parallelism
	KeyLength  uint32 // derived key length (bytes)
	SaltLength uint32 // random salt length (bytes)
}

// Hasher hashes and verifies passwords with Argon2id. Passwords are never
// stored, logged or returned; only the PHC-encoded hash is persisted.
type Hasher struct {
	params Argon2Params
}

// NewHasher validates and builds a Hasher.
func NewHasher(params Argon2Params) (*Hasher, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}
	return &Hasher{params: params}, nil
}

func (p Argon2Params) validate() error {
	switch {
	case p.Time < 1 || p.Time > 10:
		return errors.New("argon2 time must be between 1 and 10")
	case p.MemoryKiB < 8*1024 || p.MemoryKiB > 1024*1024:
		return errors.New("argon2 memory must be between 8192 and 1048576 KiB")
	case p.Threads < 1 || p.Threads > 64:
		return errors.New("argon2 threads must be between 1 and 64")
	case p.KeyLength < 16 || p.KeyLength > 128:
		return errors.New("argon2 key length must be between 16 and 128 bytes")
	case p.SaltLength < 8 || p.SaltLength > 64:
		return errors.New("argon2 salt length must be between 8 and 64 bytes")
	}
	return nil
}

// Hash derives a new salted Argon2id hash and returns its PHC string:
//
//	$argon2id$v=19$m=<KiB>,t=<passes>,p=<threads>$<salt>$<hash>
//
// The salt is generated with crypto/rand for every call; identical passwords
// therefore produce different hashes.
func (h *Hasher) Hash(password string) (string, error) {
	if len(password) > maxPasswordBytes {
		return "", ErrPasswordInvalid
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.MemoryKiB, h.params.Threads, h.params.KeyLength)
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2Version,
		h.params.MemoryKiB,
		h.params.Time,
		h.params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches encoded. It decodes the parameters
// from the encoded hash, recomputes with them and compares in constant time.
// A malformed encoded hash is an error; a password mismatch is (false, nil).
func (h *Hasher) Verify(password, encoded string) (bool, error) {
	params, salt, expected, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}
	if len(password) > maxPasswordBytes {
		// Avoid a CPU/memory DoS on absurd input while still returning the
		// normal "no match" result. Callers map that to invalid credentials.
		return false, nil
	}
	computed := argon2.IDKey([]byte(password), salt, params.Time, params.MemoryKiB, params.Threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(computed, expected) == 1, nil
}

func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	if len(encoded) == 0 || len(encoded) > maxEncodedHashLength {
		return Argon2Params{}, nil, nil, errors.New("invalid password hash length")
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, errors.New("invalid password hash format")
	}
	if parts[2] != "v="+strconv.Itoa(argon2Version) {
		return Argon2Params{}, nil, nil, errors.New("unsupported argon2 version")
	}

	var params Argon2Params
	for _, field := range strings.Split(parts[3], ",") {
		name, raw, found := strings.Cut(field, "=")
		if !found {
			return Argon2Params{}, nil, nil, errors.New("invalid argon2 parameters")
		}
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return Argon2Params{}, nil, nil, errors.New("invalid argon2 parameter value")
		}
		switch name {
		case "m":
			params.MemoryKiB = uint32(value)
		case "t":
			params.Time = uint32(value)
		case "p":
			if value > 255 {
				return Argon2Params{}, nil, nil, errors.New("invalid argon2 parallelism")
			}
			params.Threads = uint8(value)
		default:
			return Argon2Params{}, nil, nil, errors.New("unknown argon2 parameter")
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Argon2Params{}, nil, nil, errors.New("invalid argon2 salt encoding")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Argon2Params{}, nil, nil, errors.New("invalid argon2 hash encoding")
	}
	if len(salt) < 8 || len(salt) > 64 {
		return Argon2Params{}, nil, nil, errors.New("invalid argon2 salt length")
	}
	if len(key) < 16 || len(key) > 128 {
		return Argon2Params{}, nil, nil, errors.New("invalid argon2 key length")
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(key))
	if err := params.validate(); err != nil {
		return Argon2Params{}, nil, nil, err
	}
	return params, salt, key, nil
}
