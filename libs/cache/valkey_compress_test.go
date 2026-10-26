package cache_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/tanvoxyz/tanvo/libs/cache"
)

func TestValkeyCompression(t *testing.T) {
	ctx := context.Background()
	random := make([]byte, 4*cache.CompressThreshold)
	_, _ = rand.Read(random)
	repeated := bytes.Repeat([]byte(`{"amount_cents":1000,"currency":"EUR"},`), 200)
	tests := []struct {
		name   string
		value  []byte
		header byte
		// smaller reports whether the stored value is smaller than value.
		smaller bool
	}{
		{"empty", []byte{}, 0x00, false},
		{"small", []byte("hello"), 0x00, false},
		{"just below the threshold", bytes.Repeat([]byte("a"), cache.CompressThreshold-1), 0x00, false},
		{"at the threshold", bytes.Repeat([]byte("a"), cache.CompressThreshold), 0x01, true},
		{"large", repeated, 0x01, true},
		{"large incompressible", random, 0x00, false},
	}
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) {
			prefix := uniquePrefix()
			v := newValkey(t, b.url, cache.WithPrefix(prefix))
			raw, err := valkey.NewClient(valkey.MustParseURL(b.url + "?client_cache=0"))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					key := strings.ReplaceAll(tt.name, " ", "-")
					v.Set(ctx, key, tt.value, time.Hour)
					got, ok := v.Get(ctx, key)
					if !ok || !bytes.Equal(got, tt.value) {
						t.Errorf("Get(%s) = %d bytes, %v, want = the %d bytes set, true", key, len(got), ok, len(tt.value))
					}
					stored, err := raw.Do(ctx, raw.B().Get().Key(prefix+key).Build()).AsBytes()
					if err != nil {
						t.Fatal(err)
					}
					if len(stored) == 0 || stored[0] != tt.header {
						t.Fatalf("stored header = %x, want = %x", stored[:min(len(stored), 1)], tt.header)
					}
					if smaller := len(stored) < len(tt.value); smaller != tt.smaller {
						t.Errorf("stored %d bytes for %d, smaller = %v, want = %v", len(stored), len(tt.value), smaller, tt.smaller)
					}
				})
			}
		})
	}
}

func TestValkeyCompressionClientSideCache(t *testing.T) {
	ctx := context.Background()
	b := backends(t)[0]
	v := newValkey(t, b.url, cache.WithClientSideTTL(time.Minute))
	value := bytes.Repeat([]byte("abc"), cache.CompressThreshold)
	v.Set(ctx, "k", value, time.Hour)
	for range 2 {
		got, ok := v.Get(ctx, "k")
		if !ok || !bytes.Equal(got, value) {
			t.Fatalf("Get(k) = %d bytes, %v, want = the %d bytes set, true", len(got), ok, len(value))
		}
		// The caller owns what it reads, even from the client-side cache.
		got[0] = 'x'
	}
}

func TestValkeyUndecodableValuesAreMisses(t *testing.T) {
	ctx := context.Background()
	b := backends(t)[0]
	prefix := uniquePrefix()
	logs := &syncBuffer{}
	reader := sdkmetric.NewManualReader()
	v := newValkey(t, b.url, cache.WithPrefix(prefix),
		cache.WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
		cache.WithMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
	raw, err := valkey.NewClient(valkey.MustParseURL(b.url + "?client_cache=0"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for key, value := range map[string]string{
		// A value written before values had a header.
		"legacy": `{"rows":[]}`,
		"empty":  "",
		// A header claiming DEFLATE over bytes that are not.
		"corrupt": "\x01not deflate",
	} {
		if err := raw.Do(ctx, raw.B().Set().Key(prefix+key).Value(value).Build()).Error(); err != nil {
			t.Fatal(err)
		}
		if got, ok := v.Get(ctx, key); ok {
			t.Errorf("Get(%s) = %q, true, want = a miss", key, got)
		}
	}
	if got := counter(t, reader, "tanvo.cache.errors", "decode"); got != 3 {
		t.Errorf("tanvo.cache.errors{decode} = %d, want = 3", got)
	}
	if !strings.Contains(logs.String(), "cache: decode failed") {
		t.Errorf("logs = %s, want = a decode warning", logs)
	}
	// The next Set replaces the legacy value.
	v.Set(ctx, "legacy", []byte("new"), time.Hour)
	if got, ok := v.Get(ctx, "legacy"); !ok || string(got) != "new" {
		t.Errorf("Get(legacy) after Set = %q, %v, want = %q, true", got, ok, "new")
	}
	// Counters stay plain integers.
	if n, err := v.Incr(ctx, "n", time.Minute); err != nil || n != 1 {
		t.Errorf("Incr(n) = %d, %v, want = 1, nil", n, err)
	}
	if s, err := raw.Do(ctx, raw.B().Get().Key(prefix+"n").Build()).ToString(); err != nil || s != "1" {
		t.Errorf("stored counter = %q, %v, want = %q, nil", s, err, "1")
	}
}
