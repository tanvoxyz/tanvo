// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

// Package mrr derives the monthly recurring revenue of a subscription day
// by day from its subscription fees (domain §25.2).
//
// A fee bills a service period; its MRR is its net amount (less coupons,
// taxes excluded) divided by the period's length in calendar months
// ([period.Months]), on every day of the period. Fees of overlapping
// periods add up. On a day no fee covers, the subscription keeps the MRR
// of the day before, until it is terminated: from its termination day
// its MRR is zero. A subscription that is not terminated keeps the MRR of
// its last fee from then on.
package mrr

import (
	"fmt"
	"slices"
	"time"

	"github.com/cockroachdb/apd/v3"

	"github.com/tanvoxyz/tanvo/ee/analytics/period"
)

// Scale is the number of decimals of the minor unit the MRR keeps.
const Scale = 12

// Fee is a subscription fee.
type Fee struct {
	// Start is the first day of the service period.
	Start time.Time
	// End is the day after the last day of the service period.
	End time.Time
	// Net is the amount less coupons, taxes excluded, in minor units.
	Net apd.Decimal
}

// Interval is a span of days with the same MRR.
type Interval struct {
	// Start is the first day.
	Start time.Time
	// End is the day after the last day; nil when the MRR lasts.
	End *time.Time
	// Amount is the MRR in minor units, [Scale] decimals.
	Amount apd.Decimal
}

// dc divides exactly enough: 34 significant digits.
var dc = apd.BaseContext.WithPrecision(34)

// Rate returns the MRR of f: its net amount over the months of its
// service period; ok is false for a period without days.
func Rate(f Fee) (rate apd.Decimal, ok bool, err error) {
	whole, days, monthDays := period.Months(f.Start, f.End)
	if whole == 0 && days == 0 {
		return apd.Decimal{}, false, nil
	}
	// net × monthDays / (whole × monthDays + days)
	var num, den apd.Decimal
	if _, err := dc.Mul(&num, &f.Net, apd.New(int64(monthDays), 0)); err != nil {
		return apd.Decimal{}, false, fmt.Errorf("mrr: rate: %w", err)
	}
	den.SetInt64(int64(whole*monthDays + days))
	if _, err := dc.Quo(&rate, &num, &den); err != nil {
		return apd.Decimal{}, false, fmt.Errorf("mrr: rate: %w", err)
	}
	if _, err := dc.Quantize(&rate, &rate, -Scale); err != nil {
		return apd.Decimal{}, false, fmt.Errorf("mrr: rate: %w", err)
	}
	return rate, true, nil
}

// Intervals returns the MRR of a subscription with fees, oldest first,
// without days of zero MRR. terminated is the subscription's termination
// day, nil while it is not terminated.
func Intervals(fees []Fee, terminated *time.Time) ([]Interval, error) {
	type rated struct {
		start, end time.Time
		rate       apd.Decimal
	}
	var rs []rated
	var points []time.Time
	for _, f := range fees {
		r, ok, err := Rate(f)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		rs = append(rs, rated{start: f.Start, end: f.End})
		rs[len(rs)-1].rate.Set(&r)
		points = append(points, f.Start, f.End)
	}
	if len(rs) == 0 {
		return nil, nil
	}
	slices.SortFunc(points, time.Time.Compare)
	points = slices.Compact(points)

	var out []Interval
	add := func(start time.Time, end *time.Time, amount apd.Decimal) {
		if terminated != nil {
			if !start.Before(*terminated) {
				return
			}
			if end == nil || end.After(*terminated) {
				end = terminated
			}
		}
		if amount.IsZero() {
			return
		}
		if n := len(out); n > 0 && out[n-1].End != nil && out[n-1].End.Equal(start) && out[n-1].Amount.Cmp(&amount) == 0 {
			out[n-1].End = end
			return
		}
		iv := Interval{Start: start, End: end}
		iv.Amount.Set(&amount)
		out = append(out, iv)
	}
	var last apd.Decimal
	for i := range len(points) - 1 {
		start, end := points[i], points[i+1]
		var sum apd.Decimal
		covered := false
		for _, r := range rs {
			if !r.start.After(start) && r.end.After(start) {
				covered = true
				if _, err := dc.Add(&sum, &sum, &r.rate); err != nil {
					return nil, fmt.Errorf("mrr: intervals: %w", err)
				}
			}
		}
		if covered {
			last.Set(&sum)
		}
		add(start, &end, last)
	}
	add(points[len(points)-1], nil, last)
	return out, nil
}
