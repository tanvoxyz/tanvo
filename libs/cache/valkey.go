package cache

import (
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
	"github.com/valkey-io/valkey-go/valkeyotel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/tanvoxyz/tanvo/libs/core/port"
)

// ErrNotConnected is the error of [Valkey.Ping] while the client has not
// connected to the server yet.
var ErrNotConnected = errors.New("cache: not connected to the server")

// Defaults of a [Valkey] cache.
const (
	// DefaultTimeout bounds each command ([WithTimeout]).
	DefaultTimeout = time.Second
	// minBackoff and maxBackoff bound the delay between two connection
	// attempts while the server is unreachable.
	minBackoff = 100 * time.Millisecond
	maxBackoff = 30 * time.Second
	// logInterval is the least time between two warnings of the same kind.
	logInterval = 10 * time.Second
	// compressThreshold is the size from which [Valkey.Set] compresses a
	// value: below it, compression saves too little to be worth its time.
	compressThreshold = 1 << 10
)

// Headers of the values [Valkey] stores: the first byte of each says how
// the rest is encoded.
const (
	headerRaw     byte = 0x00
	headerDeflate byte = 0x01
)

// Valkey is a [port.Cache] on a Valkey or Redis server that every process
// of the installation shares. It is safe for concurrent use.
//
// The server is never required: until the client has connected, and
// whenever a command fails, a Get misses and a Set or Delete is dropped.
// Failures are logged at warn level (at most one record per 10 seconds per
// operation) and counted in the tanvo.cache.errors counter with the
// attribute tanvo.cache.operation.
//
// Each value is stored behind a one-byte header: values of at least 1 KiB
// are compressed with DEFLATE (compress/flate at BestSpeed) when that
// makes them smaller, which saves memory on the server and bandwidth to
// it. A value whose header is unknown or that does
// not decode, such as one written by a version without headers, is a miss,
// counted and logged as a decode failure; the caller's next Set replaces
// it. The counters of [Valkey.Incr] are plain integers, without a header.
type Valkey struct {
	cfg    valkeyConfig
	opt    valkey.ClientOption
	client atomic.Pointer[valkey.Client]

	incr    *valkey.Lua
	acquire *valkey.Lua
	renew   *valkey.Lua
	errs    metric.Int64Counter
	errAttr map[string]metric.AddOption
	logs    map[string]*limiter

	// writers and readers hold reusable DEFLATE encoders and decoders.
	writers sync.Pool
	readers sync.Pool

	cancel context.CancelFunc
	done   chan struct{}
	close  sync.Once
}

// valkeyConfig collects the options of [NewValkey].
type valkeyConfig struct {
	prefix     string
	csTTL      time.Duration
	timeout    time.Duration
	logger     *slog.Logger
	tp         trace.TracerProvider
	mp         metric.MeterProvider
	minBackoff time.Duration
	maxBackoff time.Duration
	slotTTL    time.Duration

	sentinelUser, sentinelPassword string
}

// ValkeyOption configures [NewValkey].
type ValkeyOption interface {
	applyValkey(*valkeyConfig)
}

// valkeyOption is a [ValkeyOption] made of a function.
type valkeyOption func(*valkeyConfig)

func (f valkeyOption) applyValkey(c *valkeyConfig) { f(c) }

// WithPrefix prefixes every key on the server, as "tanvo:", so that the
// installation can share a server with other applications.
func WithPrefix(prefix string) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) { c.prefix = prefix })
}

// WithClientSideTTL turns on server-assisted client-side caching (RESP3
// tracking): each process keeps the values it reads for at most d, and
// the server invalidates them when they change. It needs a server that
// speaks RESP3 (Redis 6 or later, Valkey). Zero, the default, turns it
// off, so that RESP2-only servers work.
//
// An invalidation reaches the other connections asynchronously: a read
// that follows a write from another connection may return the previous
// value for a short while.
func WithClientSideTTL(d time.Duration) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) { c.csTTL = d })
}

// WithSentinelAuth authenticates to the sentinels of a URL with
// master_set as user and password, which may differ from the server's own
// credentials in the URL; without it the sentinels are reached
// unauthenticated.
func WithSentinelAuth(user, password string) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) { c.sentinelUser, c.sentinelPassword = user, password })
}

// WithTimeout bounds each command, [DefaultTimeout] by default, so that a
// slow server delays a request by at most d; a command that times out is
// a miss.
func WithTimeout(d time.Duration) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) {
		if d > 0 {
			c.timeout = d
		}
	})
}

// WithSlotTTL sets the lease TTL of the slots of [Valkey.AcquireSlot],
// [port.DefaultSlotTTL] by default; a TTL that is not positive keeps it.
func WithSlotTTL(d time.Duration) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) {
		if d > 0 {
			c.slotTTL = d
		}
	})
}

// WithLogger sets the logger of the cache's warnings, slog.Default() by
// default.
func WithLogger(l *slog.Logger) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) {
		if l != nil {
			c.logger = l
		}
	})
}

// WithTracerProvider sets the provider of the client spans valkeyotel
// makes around each command, a no-op by default. The spans carry the
// command name, never its keys or values.
func WithTracerProvider(tp trace.TracerProvider) ValkeyOption {
	return valkeyOption(func(c *valkeyConfig) { c.tp = tp })
}

// NewValkey returns a cache on the server of rawURL, as parsed by
// valkey-go's ParseURL:
//
//	redis://[user:password@]host[:port][/db]   (also valkey://)
//	rediss://…                                 (also valkeys://: TLS)
//	unix://[user:password@]/path/to/socket[?db=n]
//
// with the query parameters addr (more seed addresses, for a cluster),
// master_set (the Sentinel master name; the addresses are then those of
// the sentinels), db, dial_timeout, write_timeout, protocol=2,
// client_cache=0, max_retries=0, client_name and skip_verify. A cluster is
// detected from the server.
//
// A URL that does not parse is an error. An unreachable server is not:
// NewValkey waits for a first connection attempt, at most until ctx ends;
// when it fails, it logs a warning and keeps trying in the background with
// a capped exponential backoff, and until it connects every Get misses.
// [Valkey.Close] stops it.
func NewValkey(ctx context.Context, rawURL string, opts ...ValkeyOption) (*Valkey, error) {
	opt, err := valkey.ParseURL(rawURL)
	if err != nil {
		// A url.Error repeats the URL, password included.
		if ue := (*url.Error)(nil); errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("cache: parse server URL: %w", err)
	}
	cfg := valkeyConfig{
		timeout:    DefaultTimeout,
		logger:     slog.Default(),
		minBackoff: minBackoff,
		maxBackoff: maxBackoff,
		slotTTL:    port.DefaultSlotTTL,
	}
	for _, o := range opts {
		o.applyValkey(&cfg)
	}
	if cfg.mp == nil {
		cfg.mp = noop.NewMeterProvider()
	}
	if cfg.tp == nil {
		cfg.tp = tracenoop.NewTracerProvider()
	}
	if cfg.csTTL <= 0 {
		opt.DisableCache = true
	}
	opt.Sentinel.Username, opt.Sentinel.Password = cfg.sentinelUser, cfg.sentinelPassword
	errs, err := cfg.mp.Meter(scopeName).Int64Counter("tanvo.cache.errors",
		metric.WithDescription("Failed cache commands, which callers see as misses."),
		metric.WithUnit("{error}"))
	if err != nil {
		errs, _ = noop.NewMeterProvider().Meter(scopeName).Int64Counter("tanvo.cache.errors")
	}
	v := &Valkey{
		cfg:     cfg,
		opt:     opt,
		incr:    valkey.NewLuaScript(incrScript),
		acquire: valkey.NewLuaScript(slotAcquireScript),
		renew:   valkey.NewLuaScript(slotRenewScript),
		errs:    errs,
		errAttr: map[string]metric.AddOption{},
		logs:    map[string]*limiter{},
		done:    make(chan struct{}),
	}
	v.writers.New = func() any {
		w, _ := flate.NewWriter(nil, flate.BestSpeed) // the level is valid
		return w
	}
	v.readers.New = func() any { return flate.NewReader(nil) }
	for _, op := range []string{"get", "decode", "set", "delete", "incr", "slot-acquire", "slot-renew", "slot-release", "connect"} {
		v.errAttr[op] = metric.WithAttributeSet(attribute.NewSet(AttrOperation.String(op)))
		v.logs[op] = &limiter{}
	}
	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	v.cancel = cancel
	first := make(chan struct{})
	go v.connect(bg, first)
	select {
	case <-first:
	case <-ctx.Done():
	}
	return v, nil
}

// connect creates the client, retrying until it succeeds or ctx ends; it
// closes first after the first attempt. Once created, the client
// reconnects by itself.
func (v *Valkey) connect(ctx context.Context, first chan struct{}) {
	defer close(v.done)
	once := sync.OnceFunc(func() { close(first) })
	defer once()
	backoff := v.cfg.minBackoff
	for {
		c, err := v.dial()
		if err == nil {
			if ctx.Err() != nil {
				c.Close()
				return
			}
			v.client.Store(&c)
			once()
			v.cfg.logger.DebugContext(ctx, "cache: connected", slog.Any("addresses", v.opt.InitAddress))
			return
		}
		once()
		v.fail(ctx, "connect", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, v.cfg.maxBackoff)
	}
}

// dial creates a client; a server without RESP3 makes it retry without
// client-side caching.
func (v *Valkey) dial() (valkey.Client, error) {
	otelOpts := []valkeyotel.Option{
		valkeyotel.WithTracerProvider(v.cfg.tp),
		valkeyotel.WithMeterProvider(v.cfg.mp),
	}
	c, err := valkeyotel.NewClient(v.opt, otelOpts...)
	if err != nil && errors.Is(err, valkey.ErrNoCache) && !v.opt.DisableCache {
		v.cfg.logger.Warn("cache: the server does not support client-side caching; it stays off")
		v.opt.DisableCache = true
		c, err = valkeyotel.NewClient(v.opt, otelOpts...)
	}
	return c, err
}

// Get returns the value of key, false when there is none, the client is
// not connected or the command fails.
func (v *Valkey) Get(ctx context.Context, key string) ([]byte, bool) {
	c := v.load()
	if c == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, v.cfg.timeout)
	defer cancel()
	var res valkey.ValkeyResult
	if v.cfg.csTTL > 0 {
		res = c.DoCache(ctx, c.B().Get().Key(v.cfg.prefix+key).Cache(), v.cfg.csTTL)
	} else {
		res = c.Do(ctx, c.B().Get().Key(v.cfg.prefix+key).Build())
	}
	// ToString, unlike AsBytes, returns memory that the client-side cache
	// does not share; decoding copies it again for the caller.
	s, err := res.ToString()
	if err != nil {
		if !valkey.IsValkeyNil(err) {
			v.fail(ctx, "get", err)
		}
		return nil, false
	}
	b, err := v.decode(s)
	if err != nil {
		v.fail(ctx, "decode", err)
		return nil, false
	}
	return b, true
}

// encode returns value behind its header, compressed when it is at least
// 1 KiB and compression makes it smaller. The result is a new string: a
// command that times out may still be written after Set returns and the
// caller reuses value.
func (v *Valkey) encode(value []byte) string {
	if len(value) >= compressThreshold {
		var sb strings.Builder
		sb.Grow(len(value) / 2)
		sb.WriteByte(headerDeflate)
		w := v.writers.Get().(*flate.Writer)
		w.Reset(&sb)
		_, err := w.Write(value)
		if err == nil {
			err = w.Close()
		}
		// The pooled writer must not keep the value alive.
		w.Reset(io.Discard)
		v.writers.Put(w)
		if err == nil && sb.Len() < 1+len(value) {
			return sb.String()
		}
	}
	var sb strings.Builder
	sb.Grow(1 + len(value))
	sb.WriteByte(headerRaw)
	sb.Write(value)
	return sb.String()
}

// errBadHeader is the decode failure of a value without a known header.
var errBadHeader = errors.New("cache: value without a known header")

// decode returns the value stored as s, in memory of its own.
func (v *Valkey) decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errBadHeader
	}
	switch s[0] {
	case headerRaw:
		return []byte(s[1:]), nil
	case headerDeflate:
		r := v.readers.Get().(io.ReadCloser)
		defer v.readers.Put(r)
		if err := r.(flate.Resetter).Reset(strings.NewReader(s[1:]), nil); err != nil {
			return nil, fmt.Errorf("cache: decompress: %w", err)
		}
		var buf bytes.Buffer
		buf.Grow(4 * len(s))
		_, err := buf.ReadFrom(r)
		// The pooled reader must not keep the value alive.
		_ = r.(flate.Resetter).Reset(strings.NewReader(""), nil)
		if err != nil {
			return nil, fmt.Errorf("cache: decompress: %w", err)
		}
		return buf.Bytes(), nil
	default:
		return nil, errBadHeader
	}
}

// Set stores value under key for ttl (PX, at least one millisecond),
// replacing any; a ttl that is not positive stores nothing.
func (v *Valkey) Set(ctx context.Context, key string, value []byte, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c := v.load()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, v.cfg.timeout)
	defer cancel()
	ms := max(ttl.Milliseconds(), 1)
	cmd := c.B().Set().Key(v.cfg.prefix + key).Value(v.encode(value)).PxMilliseconds(ms).Build()
	if err := c.Do(ctx, cmd).Error(); err != nil {
		v.fail(ctx, "set", err)
	}
}

// Delete removes the keys with UNLINK, one command per key so that the
// keys of a cluster may live in different slots, sent in one pipeline;
// missing keys are ignored.
func (v *Valkey) Delete(ctx context.Context, keys ...string) {
	if len(keys) == 0 {
		return
	}
	c := v.load()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, v.cfg.timeout)
	defer cancel()
	cmds := make(valkey.Commands, len(keys))
	for i, k := range keys {
		cmds[i] = c.B().Unlink().Key(v.cfg.prefix + k).Build()
	}
	for _, res := range c.DoMulti(ctx, cmds...) {
		if err := res.Error(); err != nil {
			v.fail(ctx, "delete", err)
			return
		}
	}
}

// incrScript increments KEYS[1] and, on its first increment, makes it
// expire in ARGV[1] milliseconds: one atomic step, so that a counter never
// outlives its window (EXPIRE NX needs Redis 7).
const incrScript = `local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return n`

// Incr adds one to the counter key and returns its new value; the counter
// starts at zero and expires ttl (at least one millisecond) after its
// first increment. Unlike the cache methods, a failure is an error, so
// that the caller can fall back on a count of its own: [ErrNotConnected]
// until the client has connected, else the command's error, counted and
// logged as an incr failure.
func (v *Valkey) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	c := v.load()
	if c == nil {
		return 0, ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(ctx, v.cfg.timeout)
	defer cancel()
	ms := max(ttl.Milliseconds(), 1)
	n, err := v.incr.Exec(ctx, c, []string{v.cfg.prefix + key}, []string{strconv.FormatInt(ms, 10)}).AsInt64()
	if err != nil {
		v.fail(ctx, "incr", err)
		return 0, fmt.Errorf("cache: incr: %w", err)
	}
	return n, nil
}

// Ping checks the server, for diagnostics; the cache works without it, so
// it is not a readiness check.
func (v *Valkey) Ping(ctx context.Context) error {
	c := v.load()
	if c == nil {
		return ErrNotConnected
	}
	if err := c.Do(ctx, c.B().Ping().Build()).Error(); err != nil {
		return fmt.Errorf("cache: ping: %w", err)
	}
	return nil
}

// Connected reports whether the client has connected to the server once;
// it reconnects by itself afterwards.
func (v *Valkey) Connected() bool { return v.load() != nil }

// Close stops connecting and closes the client; afterwards every Get
// misses. It is idempotent.
func (v *Valkey) Close() {
	v.close.Do(func() {
		v.cancel()
		<-v.done
		if c := v.client.Swap(nil); c != nil {
			(*c).Close()
		}
	})
}

// load returns the client, nil until connected or after Close.
func (v *Valkey) load() valkey.Client {
	if c := v.client.Load(); c != nil {
		return *c
	}
	return nil
}

// fail counts and logs a failed operation; a context the caller ended and
// a client being closed are not failures.
func (v *Valkey) fail(ctx context.Context, op string, err error) {
	if errors.Is(err, valkey.ErrClosing) || (ctx.Err() != nil && errors.Is(err, context.Canceled)) {
		return
	}
	v.errs.Add(ctx, 1, v.errAttr[op])
	if n, ok := v.logs[op].allow(time.Now()); ok {
		v.cfg.logger.WarnContext(ctx, "cache: "+op+" failed",
			slog.String("error", err.Error()), slog.Int64("suppressed", n))
	}
}

// limiter lets one event through per [logInterval] and counts the others.
type limiter struct {
	mu         sync.Mutex
	next       time.Time
	suppressed int64
}

// allow reports whether an event at now may be logged, with the number of
// events suppressed since the last one that was.
func (l *limiter) allow(now time.Time) (int64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Before(l.next) {
		l.suppressed++
		return 0, false
	}
	n := l.suppressed
	l.next, l.suppressed = now.Add(logInterval), 0
	return n, true
}
