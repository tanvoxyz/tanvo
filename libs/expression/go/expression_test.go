package expression_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tanvoxyz/tanvo/libs/money"

	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
)

func compile(t testing.TB, e *expression.Engine, src string) *expression.Program {
	t.Helper()
	p, err := e.Compile(t.Context(), src)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type stringer struct{ s string }

func (s stringer) String() string { return s.s }

func TestProperties(t *testing.T) {
	e := newEngine(t)
	p := compile(t, e, "concat(event.properties.v)")
	n := compile(t, e, "event.properties.v * 1")
	dec := money.MustParse("12.50")
	tests := []struct {
		name    string
		value   any
		text    string // the value as concat prints it
		number  string // the value as a number, "" when it is not one
		invalid bool
	}{
		{name: "string", value: "abc", text: "abc"},
		{name: "numeric string", value: "1.50", text: "1.50", number: "1.50"},
		{name: "scientific string", value: "1e3", text: "1000", number: "1000"},
		{name: "decimal", value: dec, text: "12.50", number: "12.50"},
		{name: "decimal pointer", value: &dec, text: "12.50", number: "12.50"},
		{name: "int", value: -7, text: "-7", number: "-7"},
		{name: "int64", value: int64(math.MaxInt64), text: "9223372036854775807", number: "9223372036854775807"},
		{name: "uint8", value: uint8(200), text: "200", number: "200"},
		{name: "uint64", value: uint64(math.MaxUint64), text: "18446744073709551615", number: "18446744073709551615"},
		{name: "float64", value: 1.23, text: "1.23", number: "1.23"},
		{name: "float32", value: float32(0.1), text: "0.1", number: "0.1"},
		{name: "large float", value: 1e21, text: "1e+21", number: "1e21"},
		{name: "json number", value: json.Number("42.0"), text: "42.0", number: "42.0"},
		{name: "bool", value: true, text: "true"},
		{name: "nil", value: nil, text: ""},
		{name: "stringer", value: stringer{"7"}, text: "7", number: "7"},
		{name: "map", value: map[string]any{"a": 1}, text: `{"a":1}`},
		{name: "slice", value: []int{1, 2}, text: "[1,2]"},
		{name: "NaN", value: math.NaN(), invalid: true},
		{name: "infinity", value: math.Inf(-1), invalid: true},
		{name: "invalid json number", value: json.Number("x"), invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := expression.Event{Properties: map[string]any{"v": tt.value}}
			v, err := p.Evaluate(t.Context(), ev)
			if tt.invalid {
				if !errors.Is(err, expression.ErrInvalidEvent) {
					t.Fatalf("got = %v, %v, want = ErrInvalidEvent", v, err)
				}
				return
			}
			if err != nil || v.String() != tt.text {
				t.Errorf("got = %q, %v, want = %q", v, err, tt.text)
			}
			v, err = n.Evaluate(t.Context(), ev)
			if tt.number == "" {
				if !errors.Is(err, expression.ErrExpectedDecimal) {
					t.Errorf("got = %v, %v, want = ErrExpectedDecimal", v, err)
				}
				return
			}
			if d, ok := v.Decimal(); err != nil || !ok || !d.Equal(money.MustParse(tt.number)) {
				t.Errorf("got = %v, %v, want = %s", v, err, tt.number)
			}
		})
	}
}

func TestTimestampAndCode(t *testing.T) {
	e := newEngine(t)
	p := compile(t, e, "concat(event.code, ':', event.timestamp)")
	ts := time.Date(2026, 1, 2, 3, 4, 5, 999_000_000, time.FixedZone("CET", 3600))
	v, err := p.Evaluate(t.Context(), expression.Event{Code: "api", Timestamp: ts})
	if want := fmt.Sprintf("api:%d", ts.Unix()); err != nil || v.String() != want {
		t.Errorf("got = %q, %v, want = %q", v, err, want)
	}
}

func TestValue(t *testing.T) {
	e := newEngine(t)
	v, err := compile(t, e, "1.50 * 2").Evaluate(t.Context(), expression.Event{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind() != expression.KindNumber || !v.IsNumber() || v.String() != "3.00" {
		t.Errorf("got = %v %q, want = number 3.00", v.Kind(), v)
	}
	if !v.Equal(expression.NumberValue(money.NewFromInt(3))) || v.Equal(expression.StringValue("3.00")) {
		t.Error("got = unexpected equality")
	}
	if _, ok := v.Text(); ok {
		t.Error("got = a number with text")
	}
	v, err = compile(t, e, "0.000001 * 0.1").Evaluate(t.Context(), expression.Event{})
	if err != nil || v.String() != "0.0000001" {
		t.Errorf("got = %q, %v, want = 0.0000001", v, err)
	}
	s := expression.StringValue("x")
	if _, ok := s.Decimal(); ok || s.Kind() != expression.KindString || s.Kind().String() != "string" {
		t.Errorf("got = %v, want = a string", s.Kind())
	}
}

func TestSyntaxError(t *testing.T) {
	e := newEngine(t)
	_, err := e.Compile(t.Context(), "1 + ")
	var serr *expression.SyntaxError
	if !errors.As(err, &serr) {
		t.Fatalf("got = %v, want = a *SyntaxError", err)
	}
	want := "expression: syntax error at line 1, column 5: expected function, string, variable, decimal, or unary_minus"
	if err.Error() != want {
		t.Errorf("got = %q, want = %q", err, want)
	}
	_, err = e.Compile(t.Context(), "floor(1, 2, 3)")
	want = "expression: Wrong number of arguments to function floor, expected: 1..2, provided: 3"
	if !errors.As(err, &serr) || serr.Kind != expression.SyntaxArguments || err.Error() != want {
		t.Errorf("got = %v, want = %q", err, want)
	}
}

func TestEvaluationError(t *testing.T) {
	e := newEngine(t)
	_, err := compile(t, e, "event.properties.b + 1").Evaluate(t.Context(), expression.Event{})
	if !errors.Is(err, expression.ErrMissingVariable) || errors.Is(err, expression.ErrDivisionByZero) {
		t.Errorf("got = %v, want = ErrMissingVariable", err)
	}
	if want := "expression: Variable: b not found"; err == nil || err.Error() != want {
		t.Errorf("got = %v, want = %q", err, want)
	}
	_, err = compile(t, e, "1 / 0").Evaluate(t.Context(), expression.Event{})
	if !errors.Is(err, expression.ErrDivisionByZero) {
		t.Errorf("got = %v, want = ErrDivisionByZero", err)
	}
}

// TestBatch evaluates more events than one request holds.
func TestBatch(t *testing.T) {
	e := newEngine(t)
	p := compile(t, e, "event.properties.n * 2")
	events := make([]expression.Event, 5000)
	for i := range events {
		props := map[string]any{"n": i}
		if i%1000 == 7 {
			props = nil
		}
		events[i] = expression.Event{Properties: props}
	}
	results, err := p.EvaluateBatch(t.Context(), events)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(events) {
		t.Fatalf("got = %d results, want = %d", len(results), len(events))
	}
	for i, r := range results {
		if i%1000 == 7 {
			if !errors.Is(r.Err, expression.ErrMissingVariable) {
				t.Errorf("event %d: got = %v, want = ErrMissingVariable", i, r.Err)
			}
			continue
		}
		if d, ok := r.Value.Decimal(); r.Err != nil || !ok || !d.Equal(money.NewFromInt(int64(2*i))) {
			t.Errorf("event %d: got = %v, %v, want = %d", i, r.Value, r.Err, 2*i)
		}
	}
	if results, err := p.EvaluateBatch(t.Context(), nil); err != nil || len(results) != 0 {
		t.Errorf("got = %v, %v, want = no results", results, err)
	}
}

// TestLargeEvents evaluates events larger than a request's usual size.
func TestLargeEvents(t *testing.T) {
	e := newEngine(t)
	p := compile(t, e, "concat(event.properties.s, '!')")
	big := strings.Repeat("x", 3<<20)
	results, err := p.EvaluateBatch(t.Context(), []expression.Event{
		{Properties: map[string]any{"s": big}},
		{Properties: map[string]any{"s": "small"}},
		{Properties: map[string]any{"s": big}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{big + "!", "small!", big + "!"} {
		if got := results[i].Value.String(); results[i].Err != nil || got != want {
			t.Errorf("event %d: got = %d bytes, %v, want = %d bytes", i, len(got), results[i].Err, len(want))
		}
	}
}

// TestTrap checks that a module trap fails only the events that cause it,
// and that the engine keeps working.
func TestTrap(t *testing.T) {
	e := newEngine(t, expression.WithMemoryLimit(4<<20))
	p := compile(t, e, "concat(event.properties.s, '!')")
	results, err := p.EvaluateBatch(t.Context(), []expression.Event{
		{Properties: map[string]any{"s": "a"}},
		// Beyond the memory limit.
		{Properties: map[string]any{"s": strings.Repeat("x", 3<<20)}},
		{Properties: map[string]any{"s": "b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v := results[0].Value.String(); results[0].Err != nil || v != "a!" {
		t.Errorf("event 0: got = %q, %v, want = a!", v, results[0].Err)
	}
	if !errors.Is(results[1].Err, expression.ErrTrap) || strings.Contains(results[1].Err.Error(), "\n") {
		t.Errorf("event 1: got = %q, want = ErrTrap on one line", results[1].Err)
	}
	if v := results[2].Value.String(); results[2].Err != nil || v != "b!" {
		t.Errorf("event 2: got = %q, %v, want = b!", v, results[2].Err)
	}
	if v, err := p.Evaluate(t.Context(), expression.Event{Properties: map[string]any{"s": "c"}}); err != nil || v.String() != "c!" {
		t.Errorf("got = %q, %v, want = c!", v, err)
	}
}

// TestLimits checks that input beyond the language's limits fails with its
// own errors, quickly, rather than with a trap.
func TestLimits(t *testing.T) {
	e := newEngine(t, expression.WithMemoryLimit(4<<20))
	nested := func(n int) string { return strings.Repeat("(", n) + "1" + strings.Repeat(")", n) }
	for _, c := range []struct {
		name, src string
		kind      expression.SyntaxErrorKind
		msg       string
	}{
		{"nesting", nested(expression.MaxDepth + 1), expression.SyntaxNesting, "expression: syntax error at line 1, column 129: Expression nested too deeply, expected at most 128 levels"},
		{"length", nested(100_000), expression.SyntaxLength, "expression: Expression too long: 200001 bytes, expected at most 4096"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.Compile(t.Context(), c.src)
			var serr *expression.SyntaxError
			if !errors.As(err, &serr) || serr.Kind != c.kind || err.Error() != c.msg {
				t.Errorf("got = %v, want = %s %q", err, c.kind, c.msg)
			}
		})
	}
	// The deepest nesting, and the longest flat expression, evaluate.
	for _, src := range []string{
		nested(expression.MaxDepth),
		strings.Repeat("round(", expression.MaxDepth) + "1" + strings.Repeat(")", expression.MaxDepth),
		"1" + strings.Repeat("+1", (expression.MaxSourceLen-1)/2),
		"-1" + strings.Repeat("*-1", (expression.MaxSourceLen-2)/3),
		"concat(" + strings.Repeat("1,", (expression.MaxSourceLen-9)/2) + "1)",
	} {
		if _, err := compile(t, e, src).Evaluate(t.Context(), expression.Event{}); err != nil {
			t.Errorf("%.40s…: got = %v, want = a value", src, err)
		}
	}
	p := compile(t, e, "round(1, event.properties.digits) * event.properties.x")
	for _, c := range []struct {
		props map[string]any
		want  error
	}{
		{map[string]any{"digits": 100_000_000, "x": 1}, expression.ErrDigitsOutOfRange},
		{map[string]any{"digits": -1001, "x": 1}, expression.ErrDigitsOutOfRange},
		{map[string]any{"digits": 0, "x": "1e100000"}, expression.ErrNumberOutOfRange},
		{map[string]any{"digits": 0, "x": strings.Repeat("9", expression.MaxDigits+1)}, expression.ErrNumberOutOfRange},
		{map[string]any{"digits": 0, "x": json.Number(strings.Repeat("9", expression.MaxNumberLen+1))}, expression.ErrInvalidEvent},
	} {
		if _, err := p.Evaluate(t.Context(), expression.Event{Properties: c.props}); !errors.Is(err, c.want) {
			t.Errorf("%.40v: got = %v, want = %v", c.props, err, c.want)
		}
	}
}

func TestConcurrent(t *testing.T) {
	e := newEngine(t, expression.WithMaxIdle(2))
	programs := []*expression.Program{
		compile(t, e, "event.properties.a + event.properties.b"),
		compile(t, e, "round(event.properties.a / event.properties.b, 3)"),
	}
	wants := func(a, b int) []string {
		return []string{
			fmt.Sprint(a + b),
			money.NewFromInt(int64(a)).Div(money.NewFromInt(int64(b))).Round(3, money.HalfUp).String(),
		}
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range 200 {
				a, b := g*1000+i, i+1
				ev := expression.Event{Properties: map[string]any{"a": a, "b": b}}
				for k, p := range programs {
					v, err := p.Evaluate(t.Context(), ev)
					if want := wants(a, b)[k]; err != nil || !v.Equal(expression.NumberValue(money.MustParse(want))) {
						t.Errorf("%s for a=%d b=%d: got = %v, %v, want = %s", p, a, b, v, err, want)
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

func TestClose(t *testing.T) {
	e, err := expression.NewEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p := compile(t, e, "1")
	if err := e.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(t.Context()); err != nil {
		t.Errorf("got = %v, want = a second Close to succeed", err)
	}
	if _, err := p.Evaluate(t.Context(), expression.Event{}); !errors.Is(err, expression.ErrClosed) {
		t.Errorf("got = %v, want = ErrClosed", err)
	}
	if _, err := e.Compile(t.Context(), "1"); !errors.Is(err, expression.ErrClosed) {
		t.Errorf("got = %v, want = ErrClosed", err)
	}
}

func TestCanceledContext(t *testing.T) {
	e := newEngine(t)
	p := compile(t, e, "1")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.EvaluateBatch(ctx, []expression.Event{{}}); !errors.Is(err, context.Canceled) {
		t.Errorf("got = %v, want = context.Canceled", err)
	}
}

func TestDefaultCompile(t *testing.T) {
	p, err := expression.Compile("greatest(event.properties.a, 10)")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source() != "greatest(event.properties.a, 10)" {
		t.Errorf("got = %q", p.Source())
	}
	v, err := p.Evaluate(t.Context(), expression.Event{Properties: map[string]any{"a": "12.5"}})
	if err != nil || v.String() != "12.5" {
		t.Errorf("got = %q, %v, want = 12.5", v, err)
	}
	if _, err := expression.Compile("1 +"); err == nil {
		t.Error("got = no error, want = a syntax error")
	}
}
