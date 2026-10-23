package expression_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	expression "github.com/tanvoxyz/tanvo/libs/expression/go"
)

func Example() {
	ctx := context.Background()
	p, err := expression.Compile("round(event.properties.duration_ms / 1000, 1)")
	if err != nil {
		panic(err)
	}
	v, err := p.Evaluate(ctx, expression.Event{
		Code:       "api_call",
		Timestamp:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Properties: map[string]any{"duration_ms": "1250"},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(v)
	// Output: 1.3
}

func ExampleProgram_EvaluateBatch() {
	ctx := context.Background()
	p, err := expression.Compile("event.properties.left + event.properties.right")
	if err != nil {
		panic(err)
	}
	results, err := p.EvaluateBatch(ctx, []expression.Event{
		{Properties: map[string]any{"left": "1", "right": 2}},
		{Properties: map[string]any{"left": "1"}},
	})
	if err != nil {
		panic(err)
	}
	for _, r := range results {
		if errors.Is(r.Err, expression.ErrMissingVariable) {
			fmt.Println(r.Err)
			continue
		}
		fmt.Println(r.Value)
	}
	// Output:
	// 3
	// expression: Variable: right not found
}

func ExampleCompile_syntaxError() {
	_, err := expression.Compile("1 +")
	var serr *expression.SyntaxError
	if errors.As(err, &serr) {
		fmt.Println(serr.Line, serr.Column)
		fmt.Println(err)
	}
	// Output:
	// 1 4
	// expression: syntax error at line 1, column 4: expected function, string, variable, decimal, or unary_minus
}
