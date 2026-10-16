package config

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// maxSpendCents is one billion currency units. Larger amounts are rejected
// so the split across clicks cannot overflow.
const maxSpendCents int64 = 100_000_000_000

// Spend currencies the admin accepts. There is no exchange rate.
var SpendCurrencies = []string{"USD", "EUR", "GBP", "RUB", "UAH"}

// SpendBucket is one stream and offer share of a saved cost.
// Cents is an integer so the parts add up to the entry.
// Unallocated is the whole amount when the interval had no clicks.
type SpendBucket struct {
	Stream      string `json:"stream"`
	OfferID     string `json:"offer_id"`
	NetworkID   string `json:"network_id"`
	Cents       int64  `json:"cents"`
	Unallocated bool   `json:"unallocated,omitempty"`
}

// SpendEntry is a cost saved for one campaign interval.
// From and To are a half-open UTC window. The snapshot is not recomputed later.
type SpendEntry struct {
	ID       string        `json:"id"`
	GroupID  string        `json:"group_id"`
	Zone     string        `json:"zone"`
	From     time.Time     `json:"from"`
	To       time.Time     `json:"to"`
	Cents    int64         `json:"cents"`
	Currency string        `json:"currency"`
	Unique   bool          `json:"unique"`
	Buckets  []SpendBucket `json:"buckets"`
}

// SpendWeight is one click bucket counted at save time.
type SpendWeight struct {
	Stream    string
	OfferID   string
	NetworkID string
	Hits      int64
	Uniq      int64
}

// ParseCents reads a positive amount with at most two decimal places.
// It returns cents. An empty string, a sign, or more than two decimals is an error.
func ParseCents(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		return 0, errors.New("amount must be positive")
	}
	if strings.Contains(s, ",") && strings.Contains(s, ".") {
		return 0, errors.New("amount must be positive")
	}
	s = strings.ReplaceAll(s, ",", ".")
	whole, frac, ok := strings.Cut(s, ".")
	if !ok {
		whole = s
		frac = ""
	} else if strings.Contains(frac, ".") || len(frac) > 2 {
		return 0, errors.New("amount must have at most two decimals")
	}
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w > (math.MaxInt64-99)/100 {
		return 0, errors.New("amount must be positive")
	}
	for len(frac) < 2 {
		frac += "0"
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("amount must be positive")
	}
	cents := w*100 + f
	if cents <= 0 || cents > maxSpendCents {
		return 0, errors.New("amount must be positive")
	}
	return cents, nil
}

// mulDiv returns a*b/d. b is not larger than d for a spend split, so the
// result is at most a.
func mulDiv(a, b, d int64) int64 {
	if a <= 0 || b <= 0 || d <= 0 {
		return 0
	}
	var n, m big.Int
	n.SetInt64(a)
	m.SetInt64(b)
	n.Mul(&n, &m)
	m.SetInt64(d)
	n.Quo(&n, &m)
	if !n.IsInt64() {
		return a
	}
	return n.Int64()
}

// AllowedCurrency reports whether code is one of the spend currencies.
func AllowedCurrency(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range SpendCurrencies {
		if c == code {
			return true
		}
	}
	return false
}

// Allocate splits cents across rows by hits or by unique clicks.
// The remainder goes to the heaviest row so the parts equal cents.
// No weight at all returns one unallocated bucket with the full amount.
func Allocate(cents int64, rows []SpendWeight, unique bool) []SpendBucket {
	if cents <= 0 {
		return nil
	}
	type w struct {
		row    SpendWeight
		weight int64
	}
	var use []w
	var sum int64
	for _, row := range rows {
		n := row.Hits
		if unique {
			n = row.Uniq
		}
		if n <= 0 {
			continue
		}
		use = append(use, w{row: row, weight: n})
		sum += n
	}
	if sum == 0 {
		return []SpendBucket{{Cents: cents, Unallocated: true}}
	}
	out := make([]SpendBucket, len(use))
	var used int64
	heavy := 0
	for i, row := range use {
		part := mulDiv(cents, row.weight, sum)
		out[i] = SpendBucket{
			Stream: row.row.Stream, OfferID: row.row.OfferID, NetworkID: row.row.NetworkID, Cents: part,
		}
		used += part
		if row.weight > use[heavy].weight {
			heavy = i
		}
	}
	out[heavy].Cents += cents - used
	return out
}

// SpendCovers reports whether the entry's window lies entirely inside the report window.
// A wider entry is not sliced.
func SpendCovers(entryFrom, entryTo, reportFrom, reportTo time.Time) bool {
	return !entryFrom.Before(reportFrom) && !entryTo.After(reportTo)
}

// SpendOverlaps reports whether two half-open windows share a moment.
func SpendOverlaps(aFrom, aTo, bFrom, bTo time.Time) bool {
	return aFrom.Before(bTo) && bFrom.Before(aTo)
}
