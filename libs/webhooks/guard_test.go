package webhooks

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

// source: spec/lib/lago_http_client/address_guard_spec.rb:31 (.blocked_ip?)
func TestBlocked(t *testing.T) {
	blocked := []string{
		"0.0.0.0", "10.1.2.3", "100.64.0.1", "127.0.0.1", "169.254.169.254",
		"172.16.0.1", "172.31.255.255", "192.0.0.1", "192.168.1.1", "198.18.0.1",
		"224.0.0.1", "255.255.255.255", "::", "::1", "::ffff:127.0.0.1",
		"::ffff:169.254.169.254", "64:ff9b::a00:1", "64:ff9b::a9fe:a9fe", "100::1",
		"100:0:0:1::1", "2001:db8::1", "2002:7f00:1::", "3fff::1", "5f00::1",
		"fc00::1", "fd00:ec2::254", "fe80::1", "ff02::1",
		// source: tanvo — a zone does not hide a link-local address.
		"fe80::1%eth0",
	}
	allowed := []string{"8.8.8.8", "93.184.215.14", "172.32.0.1", "2606:4700:4700::1111", "64:ff9b::5db8:d70e"}
	for _, s := range blocked {
		if !Blocked(netip.MustParseAddr(s)) {
			t.Errorf("Blocked(%s): got = false, want = true", s)
		}
	}
	for _, s := range allowed {
		if Blocked(netip.MustParseAddr(s)) {
			t.Errorf("Blocked(%s): got = true, want = false", s)
		}
	}
	if !Blocked(netip.Addr{}) {
		t.Errorf("Blocked(invalid): got = false, want = true")
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, s := range f[host] {
		out = append(out, netip.MustParseAddr(s))
	}
	return out, nil
}

// source: spec/lib/lago_http_client/address_guard_spec.rb:70 (.resolve!)
func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		addrs   []string
		want    string
		blocked bool
		fails   bool
	}{
		{name: "every address public", addrs: []string{"93.184.215.14", "2606:2800:21f:cb07:6820:80da:af6b:8b2c"}, want: "93.184.215.14"},
		{name: "one address private", addrs: []string{"93.184.215.14", "10.0.0.5"}, blocked: true},
		{name: "IPv6 loopback", addrs: []string{"::1"}, blocked: true},
		{name: "no address", fails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(context.Background(), fakeResolver{"hooks.example.com": tt.addrs}, "hooks.example.com")
			switch {
			case tt.blocked:
				if !errors.Is(err, ErrBlockedAddress) {
					t.Errorf("got = %v, want = ErrBlockedAddress", err)
				}
			case tt.fails:
				if err == nil || errors.Is(err, ErrBlockedAddress) {
					t.Errorf("got = %v, want = a resolution error", err)
				}
			case err != nil || got[0].String() != tt.want:
				t.Errorf("got = %v %v, want = %s first", got, err, tt.want)
			}
		})
	}
	// source: spec/lib/lago_http_client/client_spec.rb:740 — an IP literal is
	// checked without a lookup.
	if _, err := Resolve(context.Background(), fakeResolver{}, "::1"); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("literal ::1: got = %v, want = ErrBlockedAddress", err)
	}
}

func TestCheckURL(t *testing.T) {
	r := fakeResolver{"public.example.com": {"93.184.215.14"}, "private.example.com": {"10.0.0.1"}}
	tests := []struct {
		url          string
		allowPrivate bool
		ok           bool
	}{
		{"https://public.example.com/hook", false, true},
		{"https://private.example.com/hook", false, false},
		{"https://unresolved.example.com/hook", false, true},
		{"http://public.example.com/hook", false, false},
		{"https://169.254.169.254/latest", false, false},
		{"http://private.example.com/hook", true, true},
		{"ftp://public.example.com", true, false},
		{"not a url", true, false},
	}
	for _, tt := range tests {
		err := CheckURL(context.Background(), r, tt.url, tt.allowPrivate)
		if (err == nil) != tt.ok {
			t.Errorf("CheckURL(%s, %v): got = %v, want ok = %v", tt.url, tt.allowPrivate, err, tt.ok)
		}
	}
}
