// Package expression evaluates billable metric expressions: the language of
// lago-expression, which computes a value from an event when it is ingested.
//
// An expression is decimal arithmetic (+ - * /, unary minus, parentheses)
// over number literals, 'strings', the event's attributes (event.code,
// event.timestamp, event.properties.<name>) and the functions concat,
// round, ceil, floor, least and greatest:
//
//	round(event.properties.duration_ms / 1000, 2)
//	concat(event.properties.region, '-', event.code)
//
// The language is implemented once, in Rust (libs/expression), and compiled
// to a WebAssembly module that this package embeds and runs on wazero
// without CGO. An [Engine] compiles the module once and runs instances of it
// from a pool that is safe for concurrent use; [Compile] uses a default
// engine.
//
//	p, err := expression.Compile("event.properties.left + event.properties.right")
//	if err != nil { ... } // a *SyntaxError for an invalid expression
//	v, err := p.Evaluate(ctx, expression.Event{
//		Code:       "api_call",
//		Timestamp:  time.Now(),
//		Properties: map[string]any{"left": "1", "right": 2},
//	})
//
// A value is a number ([money.Decimal]) or a string. A property whose text is
// a number is a number, as in lago-expression. [Program.EvaluateBatch]
// evaluates many events in one call into the module, with an error per event.
//
// Expressions are user input, so the language is bounded: an expression is at
// most [MaxSourceLen] bytes and nests parentheses at most [MaxDepth] deep,
// numbers have at most [MaxDigits] significant digits and a scale within
// ±[MaxScale], and round, ceil and floor round to at most [MaxRoundDigits]
// digits. Beyond them parsing fails with a [*SyntaxError] and evaluation with
// [ErrNumberOutOfRange] or [ErrDigitsOutOfRange], before any large
// computation.
package expression
