// Package pricing turns a call's token counts into money, honestly labelled.
//
// It is deliberately split in two: Compute is a pure function of a Table (no
// I/O, no config), and table.go persists that table to
// ~/.deepseek-lens/prices.toml. The shipped table carries no rates at all —
// DeepSeek's model names were verified against its docs, its prices were not,
// and an invented number is worse than an absent one: "$0.00" reads as "this
// call was free", while "unpriced" prompts the user to configure a rate. That
// distinction is the whole design (br-GI-1-11).
package pricing

import (
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/abhisheksarkar30/deepseek-lens/internal/parse"
)

// PeakMultiplier is DeepSeek's peak/off-peak price ratio: a call placed
// inside the peak window costs this multiple of the configured off-peak
// rate.
//
// ponytail: a hardcoded constant, not a config key — the day DeepSeek moves
// this ratio, move it to config then.
const PeakMultiplier = 2.0

// peakRat is PeakMultiplier as an exact big.Rat, computed once rather than
// re-derived from the float on every priced category.
var peakRat = new(big.Rat).SetFloat64(PeakMultiplier)

// CostSource's four values. Every surface that shows a cost shows the source
// alongside it when it is not SourceConfigured.
const (
	// SourceConfigured: user-supplied rates, arithmetic performed.
	SourceConfigured = "configured"
	// SourceUnpriced: the model is in the table but its rates are not set.
	SourceUnpriced = "unpriced"
	// SourceUnknownModel: the reported model is not in the table at all.
	SourceUnknownModel = "unknown-model"
	// SourceApproximate: some tokens were priced at a rate that was not
	// configured for them — today, cache-read tokens with no cache-read rate
	// falling back to the input rate.
	SourceApproximate = "approximate"
)

// Rates is one model's price, in US dollars per million tokens, per token
// category. A nil field means "not configured" — never an implicit zero,
// which would silently understate a total.
type Rates struct {
	Input      *float64
	Output     *float64
	CacheRead  *float64
	CacheWrite *float64
}

// Set assigns one named field and reports an unknown name. The field names
// are shared with the price file and `lens prices --set`, so they are
// validated here rather than in each caller.
func (r *Rates) Set(field string, v float64) error {
	p := &v
	switch field {
	case "input":
		r.Input = p
	case "output":
		r.Output = p
	case "cache_read":
		r.CacheRead = p
	case "cache_write":
		r.CacheWrite = p
	default:
		return fmt.Errorf("pricing: set rate: unknown rate field %q (want input, output, cache_read, or cache_write)", field)
	}
	return nil
}

// Source reports how a call against these rates would be labelled — the
// table's own source column. SourceApproximate is a per-call outcome (it
// needs cache-read tokens *and* a missing cache-read rate), not a per-model
// one, so it is never reported here; the unset columns in the printed table
// carry the same information.
func (r Rates) Source() string {
	if r.Input == nil {
		return SourceUnpriced
	}
	return SourceConfigured
}

// Table is the price table, keyed by the upstream-resolved model — the name
// usage.Model reports ("deepseek-flash"), not the client's.
type Table map[string]Rates

// Table lets a Table be handed to the consumer as its price source without a
// wrapper type, so a test can inject fixed rates directly. *Loader is the
// production implementation.
func (t Table) Table() Table { return t }

// Cost is Compute's result. Amount is a whole number of micro-dollars, nil
// when the call could not be priced. Rounding happens once per call, here,
// so a total over N rows is the exact sum of N integers with no accumulated
// drift.
type Cost struct {
	Amount *int64
	Source string
}

// Compute prices one call against table, at the time it was placed. Rates
// are per million tokens and one token at $R/M costs R micro-dollars, so the
// arithmetic is `tokens × rate` summed over the four categories and rounded
// half-up to a whole micro-dollar once, at the end. When cal.IsPeak(at) — the
// window, the weekend rule, and the configured holiday calendar — every rate
// is multiplied by PeakMultiplier first, applied to the rates before
// summation as an exact big.Rat multiply, so a peak call is priced exactly
// PeakMultiplier times its off-peak amount rather than a second rounding of
// the total.
//
// The peak question is asked once, into a local, so the two multiply sites
// below cannot disagree about the instant they were given.
//
// Precedence, in order: a model absent from the table is "unknown-model" and
// a present model with no input rate is "unpriced" — neither wins to
// "approximate", because there is no rate at all to apply to any category.
// Only once a call is priced does a category with tokens but no configured
// rate of its own fall back to the input rate and downgrade the source.
func Compute(model string, usage parse.Usage, table Table, at time.Time, cal Calendar) Cost {
	r, known := table[model]
	if !known {
		return Cost{Source: SourceUnknownModel}
	}
	inputRate, ok := ratOf(r.Input)
	if !ok { // nil, NaN, or Inf: nothing to price against
		return Cost{Source: SourceUnpriced}
	}
	peak := cal.IsPeak(at)
	if peak {
		inputRate = new(big.Rat).Mul(inputRate, peakRat)
	}

	total := new(big.Rat)
	approx := false
	add := func(tokens int, rate *float64) {
		if tokens == 0 {
			return // a missing rate is irrelevant when there are no tokens
		}
		rr, ok := ratOf(rate)
		if !ok {
			rr, approx = inputRate, true
		} else if peak {
			rr = new(big.Rat).Mul(rr, peakRat)
		}
		total.Add(total, new(big.Rat).Mul(rr, new(big.Rat).SetInt64(int64(tokens))))
	}
	add(usage.InputTokens, r.Input)
	add(usage.OutputTokens, r.Output)
	add(usage.CacheReadTokens, r.CacheRead)
	add(usage.CacheCreationTokens, r.CacheWrite)

	micros := roundHalfUp(total)
	src := SourceConfigured
	if approx {
		src = SourceApproximate
	}
	return Cost{Amount: &micros, Source: src}
}

// ratOf converts a rate to an exact rational, reporting false for an unset,
// NaN, or infinite one.
//
// ponytail: big.Rat for money; a decimal library is not warranted for a
// single multiply-and-sum, and the exactness that matters here is in the
// per-call rounding, not in the last bit of a rate.
func ratOf(f *float64) (*big.Rat, bool) {
	if f == nil || math.IsNaN(*f) || math.IsInf(*f, 0) {
		return nil, false
	}
	return new(big.Rat).SetFloat64(*f), true
}

// roundHalfUp rounds r to the nearest whole micro-dollar, halves away from
// zero. Truncation would quietly round every sub-micro-dollar call to $0.00
// — pricing_test.go pins this.
func roundHalfUp(r *big.Rat) int64 {
	num, den := r.Num(), r.Denom()
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Lsh(rem, 1).CmpAbs(den) >= 0 {
		if num.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q.Int64()
}
