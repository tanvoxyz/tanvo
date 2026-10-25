package cache_test

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/tanvoxyz/tanvo/libs/cache"
	"github.com/tanvoxyz/tanvo/libs/cache/cachetest"
	"github.com/tanvoxyz/tanvo/libs/core/port"
)

func TestNamespaceConformance(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) port.Cache {
		return cache.Namespace(cache.NewMemory(1000), "test")
	}, cachetest.WithTTL(20*time.Millisecond))
}

func TestNamespacePrefixes(t *testing.T) {
	ctx := context.Background()
	m := cache.NewMemory(10)
	a := cache.Namespace(m, "a")
	b := cache.Namespace(m, "b")
	a.Set(ctx, "k", []byte("in a"), time.Hour)
	b.Set(ctx, "k", []byte("in b"), time.Hour)
	tests := []struct {
		c    port.Cache
		key  string
		want string
	}{
		{a, "k", "in a"},
		{b, "k", "in b"},
		{m, "a:k", "in a"},
		{m, "b:k", "in b"},
		{cache.Namespace(m, ""), "a:k", "in a"},
	}
	for _, tt := range tests {
		if got, ok := tt.c.Get(ctx, tt.key); !ok || string(got) != tt.want {
			t.Errorf("Get(%s) = %q, %v, want = %q, true", tt.key, got, ok, tt.want)
		}
	}
	a.Delete(ctx, "k", "missing")
	if _, ok := m.Get(ctx, "a:k"); ok {
		t.Error("Get(a:k) after a.Delete(k) ok = true, want = false")
	}
	if _, ok := m.Get(ctx, "b:k"); !ok {
		t.Error("Get(b:k) after a.Delete(k) ok = false, want = true")
	}
}

func TestNamespaceNil(t *testing.T) {
	if got := cache.Namespace(nil, "x"); got != nil {
		t.Errorf("Namespace(nil) = %v, want = nil", got)
	}
}

func TestNamespaceMetrics(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	c := cache.Namespace(cache.NewMemory(10), "plans", cache.WithMeterProvider(mp))
	c.Set(ctx, "k", []byte("v"), time.Hour)
	c.Get(ctx, "k")
	c.Get(ctx, "k")
	c.Get(ctx, "missing")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "tanvo.cache.reads" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				name, _ := dp.Attributes.Value(cache.AttrName)
				result, _ := dp.Attributes.Value(cache.AttrResult)
				if dp.Attributes.Len() != 2 {
					t.Errorf("attributes = %v, want = name and result only", dp.Attributes.ToSlice())
				}
				got[name.AsString()+"/"+result.AsString()] = dp.Value
			}
		}
	}
	want := map[string]int64{"plans/hit": 2, "plans/miss": 1}
	if len(got) != len(want) || got["plans/hit"] != 2 || got["plans/miss"] != 1 {
		t.Errorf("tanvo.cache.reads = %v, want = %v", got, want)
	}
}
