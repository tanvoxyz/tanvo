// Copyright 2026 The Tanvo Authors
// SPDX-License-Identifier: Elastic-2.0

package mrr

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/apd/v3"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func fee(start, end, net string) Fee {
	var n apd.Decimal
	if _, _, err := n.SetString(net); err != nil {
		panic(err)
	}
	return Fee{Start: day(start), End: day(end), Net: n}
}

func TestRate(t *testing.T) {
	for _, tt := range []struct {
		name string
		f    Fee
		want string
		ok   bool
	}{
		{name: "month", f: fee("2026-01-01", "2026-02-01", "1000"), want: "1000.000000000000", ok: true},
		{name: "year", f: fee("2026-01-01", "2027-01-01", "12000"), want: "1000.000000000000", ok: true},
		{name: "quarter", f: fee("2026-01-01", "2026-04-01", "3000"), want: "1000.000000000000", ok: true},
		{name: "half month of 28 days", f: fee("2026-02-01", "2026-02-15", "500"), want: "1000.000000000000", ok: true},
		{name: "week", f: fee("2026-03-02", "2026-03-09", "700"), want: "3100.000000000000", ok: true},
		{name: "thirds", f: fee("2026-01-01", "2026-04-01", "1000"), want: "333.333333333333", ok: true},
		{name: "empty", f: fee("2026-01-01", "2026-01-01", "1000")},
	} {
		got, ok, err := Rate(tt.f)
		if err != nil {
			t.Fatalf("%s: Rate() error = %v", tt.name, err)
		}
		if ok != tt.ok || (ok && got.String() != tt.want) {
			t.Errorf("%s: Rate() got = %s, %v, want = %s, %v", tt.name, got.String(), ok, tt.want, tt.ok)
		}
	}
}

func format(is []Interval) []string {
	var out []string
	for _, i := range is {
		end := "open"
		if i.End != nil {
			end = i.End.Format(time.DateOnly)
		}
		var a apd.Decimal
		_, _ = dc.Quantize(&a, &i.Amount, 0)
		out = append(out, fmt.Sprintf("%s/%s=%s", i.Start.Format(time.DateOnly), end, a.String()))
	}
	return out
}

func TestIntervals(t *testing.T) {
	term := func(s string) *time.Time { d := day(s); return &d }
	for _, tt := range []struct {
		name       string
		fees       []Fee
		terminated *time.Time
		want       []string
	}{
		{name: "no fees"},
		{
			name: "monthly fees last",
			fees: []Fee{fee("2026-01-01", "2026-02-01", "1000"), fee("2026-02-01", "2026-03-01", "1000")},
			want: []string{"2026-01-01/open=1000"},
		},
		{
			name: "upgrade within the month",
			fees: []Fee{fee("2026-01-01", "2026-02-01", "1000"), fee("2026-02-01", "2026-03-01", "2000")},
			want: []string{"2026-01-01/2026-02-01=1000", "2026-02-01/open=2000"},
		},
		{
			name: "gap carried forward",
			fees: []Fee{fee("2026-01-01", "2026-02-01", "1000"), fee("2026-03-01", "2026-04-01", "3000")},
			want: []string{"2026-01-01/2026-03-01=1000", "2026-03-01/open=3000"},
		},
		{
			name:       "terminated",
			fees:       []Fee{fee("2026-01-01", "2026-02-01", "1000"), fee("2026-02-01", "2026-02-15", "500")},
			terminated: term("2026-02-15"),
			want:       []string{"2026-01-01/2026-02-15=1000"},
		},
		{
			name:       "terminated after the last fee",
			fees:       []Fee{fee("2026-01-01", "2026-02-01", "1000")},
			terminated: term("2026-02-10"),
			want:       []string{"2026-01-01/2026-02-10=1000"},
		},
		{
			name: "overlap adds up",
			fees: []Fee{fee("2026-01-01", "2026-02-01", "1000"), fee("2026-01-15", "2026-01-22", "700")},
			want: []string{"2026-01-01/2026-01-15=1000", "2026-01-15/2026-01-22=4100", "2026-01-22/open=1000"},
		},
		{
			name: "trial of zero",
			fees: []Fee{fee("2026-01-01", "2026-01-15", "0"), fee("2026-01-15", "2026-02-15", "1000")},
			want: []string{"2026-01-15/open=1000"},
		},
		{
			name:       "terminated before its fees",
			fees:       []Fee{fee("2026-01-01", "2026-02-01", "1000")},
			terminated: term("2026-01-01"),
		},
	} {
		got, err := Intervals(tt.fees, tt.terminated)
		if err != nil {
			t.Fatalf("%s: Intervals() error = %v", tt.name, err)
		}
		if g := format(got); !slices.Equal(g, tt.want) {
			t.Errorf("%s: Intervals() got = %v, want = %v", tt.name, g, tt.want)
		}
	}
}
