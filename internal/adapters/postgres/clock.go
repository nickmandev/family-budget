package postgres

import "time"

// SystemClock is the production ports.Clock. It returns UTC instants; the
// application converts them to domain.RecordedAt at its boundary.
type SystemClock struct{}

// Now returns the current UTC instant.
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}
