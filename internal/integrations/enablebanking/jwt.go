package enablebanking

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	jwtIssuer   = "enablebanking.com"
	jwtAudience = "api.enablebanking.com"
)

type jwtSigner struct {
	privateKey *rsa.PrivateKey
}

type jwtHeader struct {
	Type      string `json:"typ"`
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type jwtClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

func newJWTSigner(privateKeyPEM []byte) (*jwtSigner, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, errors.New("PEM block is missing")
	}

	var privateKey *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		privateKey = parsed
	} else {
		parsedKey, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, errors.New("PEM does not contain a valid PKCS#1 or PKCS#8 private key")
		}
		var ok bool
		privateKey, ok = parsedKey.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key must use RSA")
		}
	}
	if privateKey.N.BitLen() < 2048 {
		return nil, errors.New("RSA private key must be at least 2048 bits")
	}
	if err := privateKey.Validate(); err != nil {
		return nil, errors.New("RSA private key is invalid")
	}
	return &jwtSigner{privateKey: privateKey}, nil
}

func (s *jwtSigner) sign(applicationID string, issuedAt time.Time, ttl time.Duration) (string, error) {
	if s == nil || s.privateKey == nil {
		return "", errors.New("signer is not initialized")
	}
	if strings.TrimSpace(applicationID) == "" {
		return "", errors.New("application ID is required")
	}
	if ttl < time.Second || ttl > maxTokenTTL {
		return "", fmt.Errorf("JWT TTL must be at least 1s and no longer than %s", maxTokenTTL)
	}
	issuedAt = issuedAt.UTC().Truncate(time.Second)
	header, err := json.Marshal(jwtHeader{Type: "JWT", Algorithm: "RS256", KeyID: applicationID})
	if err != nil {
		return "", fmt.Errorf("encode JWT header: %w", err)
	}
	claims, err := json.Marshal(jwtClaims{
		Issuer:   jwtIssuer,
		Audience: jwtAudience,
		IssuedAt: issuedAt.Unix(),
		Expires:  issuedAt.Add(ttl).Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("encode JWT claims: %w", err)
	}
	encoder := base64.RawURLEncoding
	unsigned := encoder.EncodeToString(header) + "." + encoder.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}
	return unsigned + "." + encoder.EncodeToString(signature), nil
}
