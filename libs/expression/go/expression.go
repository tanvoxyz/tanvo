package expression

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/tetratelabs/wazero"
)

// The module built from libs/expression by `pnpm nx run expression:wasm`.
//
//go:embed expression.wasm
var module []byte

const (
	// A request holds at most batchEvents events, and more than one only
	// while it is shorter than batchBytes.
	batchEvents = 1024
	batchBytes  = 1 << 20
	// An instance whose request buffer or memory grew beyond these is not
	// kept for later calls.
	maxRetainedRequest = 2 * batchBytes
	maxRetainedMemory  = 32 << 20

	wasmPage = 64 << 10
)

// ErrClosed is returned by the calls of an [Engine] that is closed.
var ErrClosed = errors.New("expression: engine closed")

type config struct {
	maxIdle     int
	memoryLimit uint32 // in pages
}

// Option configures an [Engine].
type Option func(*config)

// WithMaxIdle sets how many idle module instances the engine keeps for later
// calls; the default is GOMAXPROCS. Concurrent calls beyond it instantiate
// the module again, which takes tens of microseconds.
func WithMaxIdle(n int) Option {
	return func(c *config) { c.maxIdle = max(n, 0) }
}

// WithMemoryLimit sets the most memory one module instance may use, rounded
// up to 64 KiB pages; the default is 256 MiB. A call that needs more fails
// with [ErrTrap]; the language's limits (see [MaxDigits]) keep an evaluation
// within a few megabytes, so only a batch of very large events comes near.
func WithMemoryLimit(bytes uint64) Option {
	return func(c *config) {
		pages := min((bytes+wasmPage-1)/wasmPage, 65536)
		c.memoryLimit = uint32(max(pages, 32))
	}
}

// Engine runs the expression module: it compiles it once and keeps a pool of
// instances. An Engine is safe for concurrent use.
type Engine struct {
	runtime wazero.Runtime
	module  wazero.CompiledModule
	idle    chan *instance
	closed  atomic.Bool
}

// NewEngine compiles the embedded module, to native code where wazero's
// compiler supports the platform (amd64 and arm64), and to wazero's
// interpreter elsewhere.
func NewEngine(ctx context.Context, opts ...Option) (*Engine, error) {
	c := config{maxIdle: runtime.GOMAXPROCS(0), memoryLimit: (256 << 20) / wasmPage}
	for _, o := range opts {
		o(&c)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(c.memoryLimit))
	compiled, err := rt.CompileModule(ctx, module)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("expression: compile module: %w", err), rt.Close(ctx))
	}
	return &Engine{runtime: rt, module: compiled, idle: make(chan *instance, c.maxIdle)}, nil
}

// Close releases the engine's instances and compiled module. Calls that are
// running fail; later calls return [ErrClosed].
func (e *Engine) Close(ctx context.Context) error {
	if e.closed.Swap(true) {
		return nil
	}
	if err := e.runtime.Close(ctx); err != nil {
		return fmt.Errorf("expression: close: %w", err)
	}
	return nil
}

// get returns an idle instance, or a new one.
func (e *Engine) get(ctx context.Context) (*instance, error) {
	if e.closed.Load() {
		return nil, ErrClosed
	}
	select {
	case in := <-e.idle:
		return in, nil
	default:
	}
	// An anonymous module can be instantiated any number of times.
	mod, err := e.runtime.InstantiateModule(ctx, e.module, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		if e.closed.Load() {
			return nil, ErrClosed
		}
		return nil, fmt.Errorf("expression: instantiate module: %w", err)
	}
	in, err := newInstance(mod)
	if err != nil {
		return nil, errors.Join(err, mod.Close(ctx))
	}
	return in, nil
}

// put keeps in for later calls, unless the pool is full or in grew large.
func (e *Engine) put(ctx context.Context, in *instance) {
	if !e.closed.Load() && in.mem.Size() <= maxRetainedMemory {
		select {
		case e.idle <- in:
			return
		default:
		}
	}
	e.discard(ctx, in)
}

// discard closes an instance that is not kept, such as one that trapped.
func (e *Engine) discard(ctx context.Context, in *instance) {
	// The instance is unreachable after this; closing it frees its memory
	// now rather than with the runtime.
	_ = in.mod.Close(ctx)
}

// Compile parses src. An invalid expression is a [*SyntaxError], one longer
// than [MaxSourceLen] bytes or nested deeper than [MaxDepth] included.
func (e *Engine) Compile(ctx context.Context, src string) (*Program, error) {
	if len(src) > MaxSourceLen {
		// As the module reports it, without copying src into it.
		return nil, &SyntaxError{
			Kind:    SyntaxLength,
			Message: fmt.Sprintf("Expression too long: %d bytes, expected at most %d", len(src), MaxSourceLen),
		}
	}
	in, err := e.get(ctx)
	if err != nil {
		return nil, err
	}
	serr, err := in.parseSource(ctx, src)
	if err != nil {
		e.discard(ctx, in)
		if errors.Is(err, errMalformed) || ctx.Err() != nil || e.closed.Load() {
			return nil, e.callError(ctx, err)
		}
		// The module's limits leave running out of memory.
		msg, _, _ := strings.Cut(err.Error(), "\n")
		return nil, fmt.Errorf("%w: parse: %s", ErrTrap, msg)
	}
	e.put(ctx, in)
	if serr != nil {
		return nil, serr
	}
	return &Program{engine: e, src: src}, nil
}

// callError describes a failed call into the module.
func (e *Engine) callError(ctx context.Context, err error) error {
	switch {
	case e.closed.Load():
		return ErrClosed
	case ctx.Err() != nil:
		return fmt.Errorf("expression: %w", context.Cause(ctx))
	default:
		return fmt.Errorf("expression: %w", err)
	}
}

// defaultEngine is the engine of [Compile], created on first use.
var defaultEngine = sync.OnceValues(func() (*Engine, error) {
	return NewEngine(context.Background())
})

// Compile parses src with a default engine that lives as long as the
// process. An invalid expression is a [*SyntaxError].
func Compile(src string) (*Program, error) {
	e, err := defaultEngine()
	if err != nil {
		return nil, err
	}
	return e.Compile(context.Background(), src)
}

// Program is a parsed expression. A Program is safe for concurrent use.
type Program struct {
	engine *Engine
	src    string
}

// Source returns the expression's text.
func (p *Program) Source() string { return p.src }

// String returns the expression's text.
func (p *Program) String() string { return p.src }

// Evaluate evaluates the expression for ev. An event the expression cannot be
// evaluated for is an [*EvaluationError]; other errors are failures of the
// engine or of ctx.
func (p *Program) Evaluate(ctx context.Context, ev Event) (Value, error) {
	var out [1]Result
	if err := p.evaluate(ctx, []Event{ev}, out[:]); err != nil {
		return Value{}, err
	}
	return out[0].Value, out[0].Err
}

// EvaluateBatch evaluates the expression for each event, with one result per
// event in order. A result's error is an [*EvaluationError]; the returned
// error is a failure of the engine or of ctx, after which no result is valid.
func (p *Program) EvaluateBatch(ctx context.Context, events []Event) ([]Result, error) {
	out := make([]Result, len(events))
	if err := p.evaluate(ctx, events, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Program) evaluate(ctx context.Context, events []Event, out []Result) error {
	for len(events) > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("expression: %w", context.Cause(ctx))
		}
		n, err := p.chunk(ctx, events, out, false)
		if err != nil {
			return err
		}
		events, out = events[n:], out[n:]
	}
	return nil
}

// chunk evaluates the events that fit in one request, and returns how many.
// When the module traps on a request of several events, each of them is
// evaluated alone so that only the failing ones fail; single is set then.
func (p *Program) chunk(ctx context.Context, events []Event, out []Result, single bool) (int, error) {
	e := p.engine
	in, err := e.get(ctx)
	if err != nil {
		return 0, err
	}
	if single {
		events = events[:1]
	}
	n, serr, err := in.evaluateBatch(ctx, p.src, events, out)
	if err == nil {
		e.put(ctx, in)
		if serr != nil {
			// The program parsed when it was compiled.
			return 0, fmt.Errorf("expression: %w", serr)
		}
		return n, nil
	}
	e.discard(ctx, in)
	if errors.Is(err, errMalformed) || ctx.Err() != nil || e.closed.Load() {
		return 0, e.callError(ctx, err)
	}
	if n == 1 {
		// wazero follows the trap's message with the module's stack.
		msg, _, _ := strings.Cut(err.Error(), "\n")
		out[0] = Result{Err: &EvaluationError{Kind: ErrorTrap, Message: msg}}
		return 1, nil
	}
	for i := range n {
		if _, err := p.chunk(ctx, events[i:], out[i:], true); err != nil {
			return 0, err
		}
	}
	return n, nil
}
