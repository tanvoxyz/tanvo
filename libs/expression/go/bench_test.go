package expression_test

import (
	"fmt"
	"testing"
	"time"

	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
)

// benchExpressions are a sum and an expression with a division, which
// bigdecimal computes to 100 significant digits.
var benchExpressions = []struct{ name, src string }{
	{"sum", "event.properties.duration_ms + event.properties.rate"},
	{"division", "round(event.properties.duration_ms / 1000 * event.properties.rate, 2)"},
}

func benchEvents(n int) []expression.Event {
	events := make([]expression.Event, n)
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range events {
		events[i] = expression.Event{
			Code:      "api_call",
			Timestamp: ts.Add(time.Duration(i) * time.Second),
			Properties: map[string]any{
				"duration_ms": fmt.Sprint(1000 + i),
				"rate":        "0.25",
				"region":      "eu-west-1",
			},
		}
	}
	return events
}

func BenchmarkEvaluate(b *testing.B) {
	e := newEngine(b)
	ev := benchEvents(1)[0]
	for _, x := range benchExpressions {
		b.Run(x.name, func(b *testing.B) {
			p := compile(b, e, x.src)
			for b.Loop() {
				if _, err := p.Evaluate(b.Context(), ev); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkEvaluateParallel(b *testing.B) {
	e := newEngine(b)
	ev := benchEvents(1)[0]
	for _, x := range benchExpressions {
		b.Run(x.name, func(b *testing.B) {
			p := compile(b, e, x.src)
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := p.Evaluate(b.Context(), ev); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}

func BenchmarkEvaluateBatch(b *testing.B) {
	e := newEngine(b)
	for _, x := range benchExpressions {
		for _, n := range []int{100, 1000} {
			b.Run(fmt.Sprintf("%s/%d", x.name, n), func(b *testing.B) {
				p := compile(b, e, x.src)
				events := benchEvents(n)
				for b.Loop() {
					if _, err := p.EvaluateBatch(b.Context(), events); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/event")
			})
		}
	}
}

func BenchmarkCompile(b *testing.B) {
	e := newEngine(b)
	src := benchExpressions[1].src
	for b.Loop() {
		if _, err := e.Compile(b.Context(), src); err != nil {
			b.Fatal(err)
		}
	}
}
