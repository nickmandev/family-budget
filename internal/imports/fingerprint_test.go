package imports

import (
	"strings"
	"testing"
	"time"
)

func TestFingerprintUsesExternalIDWhenAvailable(t *testing.T) {
	entry := Entry{ExternalID: "id-is-not-used-here", BookedDate: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	if got, want := Fingerprint(" FiBank ", "entry-123", entry), "fibank|entry-123"; got != want {
		t.Fatalf("Fingerprint = %q, want %q", got, want)
	}
}

func TestFingerprintFallbackIsStableAndUnique(t *testing.T) {
	entry := Entry{
		BookedDate: time.Date(2026, 10, 1, 14, 30, 0, 0, time.FixedZone("offset", 2*60*60)),
		Direction:  DirectionDebit, AmountMinor: 1234, Currency: "eur",
		Merchant: "  Corner\tSHOP ", Description: " CARD   payment ", Reference: " ref 1 ", TransactionCode: "POS",
	}
	first := Fingerprint("FIBANK", "", entry)
	entry.Merchant = "corner shop"
	entry.Description = "card payment"
	entry.Reference = "REF 1"
	second := Fingerprint(" fibank ", "", entry)
	if first != second {
		t.Fatalf("normalized equivalent fingerprints differ: %q / %q", first, second)
	}
	if !strings.HasPrefix(first, "fibank|h:") || len(strings.TrimPrefix(first, "fibank|h:")) != 64 {
		t.Fatalf("fallback fingerprint = %q", first)
	}
	entry.AmountMinor++
	if changed := Fingerprint("fibank", "", entry); changed == first {
		t.Fatal("different amount produced the same fallback fingerprint")
	}
}
