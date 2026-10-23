package expression

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/tetratelabs/wazero/api"

	"github.com/tanvoxyz/tanvo/libs/money"
)

// This file speaks the module's interface, which src/abi.rs documents: a
// request is written into a buffer of expr_alloc, expr_parse or
// expr_evaluate answers with a buffer packed into a uint64, and both are
// freed with expr_free.

const (
	tagString = 0
	tagNumber = 1

	resultNumber = 0
	resultString = 1
	resultError  = 2
)

// errMalformed is a request or response that does not follow the encoding:
// a bug of this package or of the module.
var errMalformed = errors.New("expression: malformed module message")

// encoder builds a request.
type encoder struct{ b []byte }

func (e *encoder) u32(n int) {
	e.b = binary.LittleEndian.AppendUint32(e.b, uint32(n))
}

func (e *encoder) str(s string) {
	e.u32(len(s))
	e.b = append(e.b, s...)
}

func (e *encoder) value(tag byte, text string) {
	e.b = append(e.b, tag)
	e.str(text)
}

// number appends a number value; its text may be no decimal ("NaN"), which
// the module reports as an invalid event.
func (e *encoder) number(text string) { e.value(tagNumber, text) }

func (e *encoder) event(ev *Event) {
	e.str(ev.Code)
	e.b = append(e.b, tagNumber)
	e.u32(0)
	at := len(e.b)
	e.b = strconv.AppendInt(e.b, ev.Timestamp.Unix(), 10)
	binary.LittleEndian.PutUint32(e.b[at-4:], uint32(len(e.b)-at))
	e.u32(len(ev.Properties))
	for name, v := range ev.Properties {
		e.str(name)
		e.property(v)
	}
}

// property appends a property value, converted as [Event.Properties] says.
func (e *encoder) property(v any) {
	switch v := v.(type) {
	case string:
		e.value(tagString, v)
	case money.Decimal:
		e.number(v.String())
	case *money.Decimal:
		if v == nil {
			e.value(tagString, "")
		} else {
			e.number(v.String())
		}
	case json.Number:
		e.number(string(v))
	case int:
		e.number(strconv.FormatInt(int64(v), 10))
	case int8:
		e.number(strconv.FormatInt(int64(v), 10))
	case int16:
		e.number(strconv.FormatInt(int64(v), 10))
	case int32:
		e.number(strconv.FormatInt(int64(v), 10))
	case int64:
		e.number(strconv.FormatInt(v, 10))
	case uint:
		e.number(strconv.FormatUint(uint64(v), 10))
	case uint8:
		e.number(strconv.FormatUint(uint64(v), 10))
	case uint16:
		e.number(strconv.FormatUint(uint64(v), 10))
	case uint32:
		e.number(strconv.FormatUint(uint64(v), 10))
	case uint64:
		e.number(strconv.FormatUint(v, 10))
	case float32:
		e.float(float64(v), 32)
	case float64:
		e.float(v, 64)
	case bool:
		e.value(tagString, strconv.FormatBool(v))
	case nil:
		e.value(tagString, "")
	case fmt.Stringer:
		e.value(tagString, v.String())
	default:
		b, err := json.Marshal(v)
		if err != nil {
			e.value(tagString, fmt.Sprint(v))
		} else {
			e.value(tagString, string(b))
		}
	}
}

func (e *encoder) float(f float64, bits int) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		e.number("NaN")
		return
	}
	e.number(strconv.FormatFloat(f, 'g', -1, bits))
}

// decoder reads a response.
type decoder struct {
	b   []byte
	err error
}

func (d *decoder) bytes(n int) []byte {
	if d.err != nil || n > len(d.b) {
		d.err = errMalformed
		return nil
	}
	v := d.b[:n]
	d.b = d.b[n:]
	return v
}

func (d *decoder) u8() byte {
	if b := d.bytes(1); b != nil {
		return b[0]
	}
	return 0
}

func (d *decoder) u32() int {
	if b := d.bytes(4); b != nil {
		return int(binary.LittleEndian.Uint32(b))
	}
	return 0
}

func (d *decoder) str() string { return string(d.bytes(d.u32())) }

// parseHeader reads a parse header: nil when the expression parsed.
func (d *decoder) parseHeader() *SyntaxError {
	if d.u8() == 0 {
		return nil
	}
	e := &SyntaxError{Kind: SyntaxErrorKind(d.u8())}
	e.Line, e.Column, e.Offset = d.u32(), d.u32(), d.u32()
	e.Message = d.str()
	return e
}

func (d *decoder) result() Result {
	switch d.u8() {
	case resultNumber:
		text := d.bytes(d.u32())
		if d.err != nil {
			return Result{}
		}
		n, err := money.Parse(string(text))
		if err != nil {
			d.err = fmt.Errorf("%w: number %q: %w", errMalformed, text, err)
			return Result{}
		}
		return Result{Value: NumberValue(n)}
	case resultString:
		return Result{Value: StringValue(d.str())}
	case resultError:
		kind := ErrorKind(d.u8())
		return Result{Err: &EvaluationError{Kind: kind, Message: d.str()}}
	default:
		d.err = errMalformed
		return Result{}
	}
}

// instance is one instantiation of the module, used by one goroutine at a
// time. It keeps a request buffer in the module's memory and grows it as
// requests need.
type instance struct {
	mod                          api.Module
	mem                          api.Memory
	alloc, free, parse, evaluate api.Function
	stack                        [2]uint64
	buf, bufCap                  uint32
	enc                          encoder
}

func newInstance(mod api.Module) (*instance, error) {
	in := &instance{
		mod:      mod,
		mem:      mod.Memory(),
		alloc:    mod.ExportedFunction("expr_alloc"),
		free:     mod.ExportedFunction("expr_free"),
		parse:    mod.ExportedFunction("expr_parse"),
		evaluate: mod.ExportedFunction("expr_evaluate"),
	}
	if in.mem == nil || in.alloc == nil || in.free == nil || in.parse == nil || in.evaluate == nil {
		return nil, errors.New("expression: the module lacks an export of its interface")
	}
	return in, nil
}

func (in *instance) call(ctx context.Context, fn api.Function, a, b uint64) (uint64, error) {
	in.stack[0], in.stack[1] = a, b
	if err := fn.CallWithStack(ctx, in.stack[:]); err != nil {
		return 0, err
	}
	return in.stack[0], nil
}

// request calls fn with the request in in.enc and passes its response to
// read before freeing it. An error is a failure of the module, after which
// the instance must not be used.
func (in *instance) request(ctx context.Context, fn api.Function, read func(*decoder)) error {
	req := in.enc.b
	if len(req) > math.MaxInt32 {
		return errors.New("expression: request too large")
	}
	n := uint32(len(req))
	if n > in.bufCap {
		size := max(n, 2*in.bufCap, 4096)
		if in.bufCap > 0 {
			if _, err := in.call(ctx, in.free, uint64(in.buf), uint64(in.bufCap)); err != nil {
				return err
			}
			in.buf, in.bufCap = 0, 0
		}
		ptr, err := in.call(ctx, in.alloc, uint64(size), 0)
		if err != nil {
			return err
		}
		in.buf, in.bufCap = uint32(ptr), size
	}
	if !in.mem.Write(in.buf, req) {
		return errMalformed
	}
	packed, err := in.call(ctx, fn, uint64(in.buf), uint64(n))
	if err != nil {
		return err
	}
	if packed == 0 {
		return errMalformed
	}
	ptr, size := uint32(packed>>32), uint32(packed)
	resp, ok := in.mem.Read(ptr, size)
	if !ok {
		return errMalformed
	}
	d := decoder{b: resp}
	read(&d)
	if d.err == nil && len(d.b) != 0 {
		d.err = errMalformed
	}
	if _, err := in.call(ctx, in.free, uint64(ptr), uint64(size)); err != nil {
		return err
	}
	return d.err
}

// parseSource asks the module to parse src.
func (in *instance) parseSource(ctx context.Context, src string) (*SyntaxError, error) {
	in.enc.b = append(in.enc.b[:0], src...)
	var serr *SyntaxError
	err := in.request(ctx, in.parse, func(d *decoder) { serr = d.parseHeader() })
	return serr, err
}

// evaluateBatch evaluates src for the events that fit in one request, into
// out, and returns how many.
func (in *instance) evaluateBatch(ctx context.Context, src string, events []Event, out []Result) (int, *SyntaxError, error) {
	in.enc.b = in.enc.b[:0]
	in.enc.str(src)
	in.enc.u32(0)
	at := len(in.enc.b)
	n := 0
	for n < len(events) && n < batchEvents && (n == 0 || len(in.enc.b) < batchBytes) {
		in.enc.event(&events[n])
		n++
	}
	binary.LittleEndian.PutUint32(in.enc.b[at-4:], uint32(n))
	var serr *SyntaxError
	err := in.request(ctx, in.evaluate, func(d *decoder) {
		if serr = d.parseHeader(); serr != nil {
			return
		}
		if d.u32() != n {
			d.err = errMalformed
			return
		}
		for i := range n {
			out[i] = d.result()
		}
	})
	// A large batch must not keep its buffer.
	if cap(in.enc.b) > maxRetainedRequest {
		in.enc.b = nil
	}
	return n, serr, err
}
