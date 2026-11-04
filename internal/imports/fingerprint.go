package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Fingerprint creates the stable per-account import identity for one entry.
func Fingerprint(bankCode, externalID string, entry Entry) string {
	bankCode = strings.ToLower(strings.TrimSpace(bankCode))
	if externalID != "" {
		return bankCode + "|" + externalID
	}
	fields := []string{
		entry.BookedDate.UTC().Format(time.DateOnly),
		strings.ToUpper(strings.TrimSpace(string(entry.Direction))),
		fmt.Sprintf("%d", entry.AmountMinor),
		strings.ToUpper(strings.TrimSpace(entry.Currency)),
		normalizeFingerprintText(entry.Merchant),
		normalizeFingerprintText(entry.Description),
		normalizeFingerprintText(entry.Reference),
		// TransactionCode is the statement's available occurrence discriminator
		// when no external row identifier is supplied.
		normalizeFingerprintText(entry.TransactionCode),
	}
	var canonical strings.Builder
	for _, field := range fields {
		fmt.Fprintf(&canonical, "%d:%s", len(field), field)
	}
	digest := sha256.Sum256([]byte(canonical.String()))
	return bankCode + "|h:" + hex.EncodeToString(digest[:])
}

func normalizeFingerprintText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
