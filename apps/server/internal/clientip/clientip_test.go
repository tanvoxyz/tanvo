package clientip

import (
	"net/http"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	r := New(mustProxy(t, "203.0.113.0/24"), mustProxy(t, "198.51.100.9"))
	tests := []struct {
		name     string
		remote   string
		forward  []string
		clientIP string
		want     string
	}{
		{name: "direct client", remote: "198.51.100.1:5000", want: "198.51.100.1"},
		{name: "direct client ignores headers", remote: "198.51.100.1:5000", forward: []string{"192.0.2.1"}, want: "198.51.100.1"},
		{name: "loopback peer without headers", remote: "127.0.0.1:5000", want: "127.0.0.1"},
		{name: "ipv6 peer", remote: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "mapped ipv4 peer", remote: "[::ffff:192.0.2.4]:443", want: "192.0.2.4"},
		{name: "bare peer address", remote: "192.0.2.4", want: "192.0.2.4"},
		{name: "invalid peer", remote: "pipe", want: ""},
		{name: "one private proxy", remote: "10.0.0.2:80", forward: []string{"192.0.2.1"}, want: "192.0.2.1"},
		{name: "nearest untrusted wins", remote: "10.0.0.2:80", forward: []string{"192.0.2.66, 192.0.2.1, 172.16.4.4"}, want: "192.0.2.1"},
		{name: "repeated headers", remote: "10.0.0.2:80", forward: []string{"192.0.2.66", "192.0.2.1"}, want: "192.0.2.1"},
		{name: "configured range", remote: "10.0.0.2:80", forward: []string{"192.0.2.1, 203.0.113.50"}, want: "192.0.2.1"},
		{name: "configured address", remote: "198.51.100.9:80", forward: []string{"192.0.2.1"}, want: "192.0.2.1"},
		{name: "all trusted gives the furthest", remote: "10.0.0.2:80", forward: []string{"192.168.1.1, 10.1.1.1"}, want: "192.168.1.1"},
		{name: "invalid entries and netmasks skipped", remote: "10.0.0.2:80", forward: []string{"unknown, 192.0.2.0/24, 192.0.2.1"}, want: "192.0.2.1"},
		{name: "client ip header", remote: "10.0.0.2:80", clientIP: "192.0.2.7", want: "192.0.2.7"},
		{name: "client ip among forwarded", remote: "10.0.0.2:80", forward: []string{"192.0.2.7, 10.0.0.9"}, clientIP: "192.0.2.7", want: "192.0.2.7"},
		{name: "client ip spoof ignored", remote: "10.0.0.2:80", forward: []string{"192.0.2.1"}, clientIP: "192.0.2.7", want: "192.0.2.1"},
		{name: "private ipv6 proxy", remote: "[fd00::1]:80", forward: []string{"2001:db8::5"}, want: "2001:db8::5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tt.forward {
				h.Add("X-Forwarded-For", v)
			}
			if tt.clientIP != "" {
				h.Set("Client-Ip", tt.clientIP)
			}
			if got := r.Resolve(tt.remote, h); got != tt.want {
				t.Errorf("Resolve(%q, %v) got = %q, want = %q", tt.remote, h, got, tt.want)
			}
		})
	}
}

func TestZeroResolverTrustsNothing(t *testing.T) {
	h := http.Header{"X-Forwarded-For": {"192.0.2.1"}}
	if got := (&Resolver{}).Resolve("127.0.0.1:80", h); got != "127.0.0.1" {
		t.Errorf("Resolve() got = %q, want = %q", got, "127.0.0.1")
	}
}

func TestParseProxy(t *testing.T) {
	tests := []struct {
		in, want string
		err      bool
	}{
		{in: "203.0.113.7", want: "203.0.113.7/32"},
		{in: " 203.0.113.9/24 ", want: "203.0.113.0/24"},
		{in: "2001:db8::/32", want: "2001:db8::/32"},
		{in: "::ffff:10.0.0.1", want: "10.0.0.1/32"},
		{in: "proxy.internal", err: true},
		{in: "10.0.0.0/33", err: true},
	}
	for _, tt := range tests {
		var p Proxy
		err := p.UnmarshalText([]byte(tt.in))
		if (err != nil) != tt.err {
			t.Errorf("UnmarshalText(%q) error got = %v, want error = %v", tt.in, err, tt.err)
			continue
		}
		if got := p.String(); !tt.err && got != tt.want {
			t.Errorf("UnmarshalText(%q) got = %q, want = %q", tt.in, got, tt.want)
		}
	}
}

func mustProxy(t *testing.T, s string) Proxy {
	t.Helper()
	p, err := ParseProxy(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProxiesUnmarshalText(t *testing.T) {
	var ps Proxies
	if err := ps.UnmarshalText([]byte("10.1.0.0/16, 203.0.113.7,")); err != nil {
		t.Fatalf("UnmarshalText() error got = %v, want = nil", err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.String())
	}
	if want := "10.1.0.0/16 203.0.113.7/32"; strings.Join(got, " ") != want {
		t.Errorf("UnmarshalText() got = %v, want = %s", got, want)
	}
	if err := ps.UnmarshalText([]byte("10.1.0.0/16, nope")); err == nil {
		t.Error("UnmarshalText() of an invalid entry error got = nil, want = an error")
	}
}
