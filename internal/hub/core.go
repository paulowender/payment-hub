package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

var errInvalid = errors.New("invalid_request")
var errConflict = errors.New("idempotency_conflict")
var errNotFound = errors.New("not_found")
var errFunds = errors.New("insufficient_credits")

func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func validInterval(unit string, n int) bool {
	return unit == "day" && n >= 1 && n <= 365 || unit == "month" && n >= 1 && n <= 120
}
func cycleAt(anchor time.Time, unit string, n, cycle int) time.Time {
	if unit == "day" {
		return anchor.AddDate(0, 0, n*cycle)
	}
	first := time.Date(anchor.Year(), anchor.Month(), 1, anchor.Hour(), anchor.Minute(), anchor.Second(), anchor.Nanosecond(), time.UTC).AddDate(0, n*cycle, 0)
	last := first.AddDate(0, 1, -1).Day()
	day := anchor.Day()
	if day > last {
		day = last
	}
	return first.AddDate(0, 0, day-1)
}

// Monetary amounts and credit quantities are bounded to keep sums and provider requests practical.
func validAmount(n int64) bool  { return n > 0 && n <= 99999999 }
func validCredits(n int64) bool { return n >= 0 && n <= 1000000000 }
