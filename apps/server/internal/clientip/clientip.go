package clientip

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// DefaultTrusted are the proxies trusted by default, those of
// ActionDispatch::RemoteIp::TRUSTED_PROXIES (actionpack 8.0
// action_dispatch/middleware/remote_ip.rb:40): the addresses that the IP
// specification makes loopback or private, which are never the client's
// in production.
func DefaultTrusted() []netip.Prefix {
	return []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
}

// Proxy is a trusted proxy, an address or a CIDR range. It decodes from
// text ("203.0.113.7", "203.0.113.0/24", "2001:db8::/32").
type Proxy struct{ netip.Prefix }

// ParseProxy parses an address or a CIDR range.
func ParseProxy(s string) (Proxy, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return Proxy{}, fmt.Errorf("trusted proxy %q: %w", s, err)
		}
		return Proxy{p.Masked()}, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return Proxy{}, fmt.Errorf("trusted proxy %q: %w", s, err)
	}
	a = a.Unmap()
	return Proxy{netip.PrefixFrom(a, a.BitLen())}, nil
}

// UnmarshalText implements [encoding.TextUnmarshaler].
func (p *Proxy) UnmarshalText(b []byte) error {
	v, err := ParseProxy(string(b))
	if err != nil {
		return err
	}
	*p = v
	return nil
}

// MarshalText implements [encoding.TextMarshaler].
func (p Proxy) MarshalText() ([]byte, error) { return p.Prefix.MarshalText() }

// Proxies are trusted proxies. They decode from a comma-separated list
// ("10.1.0.0/16, 203.0.113.7") as well as from a list of texts.
type Proxies []Proxy

// UnmarshalText implements [encoding.TextUnmarshaler].
func (ps *Proxies) UnmarshalText(b []byte) error {
	var out Proxies
	for f := range strings.SplitSeq(string(b), ",") {
		if strings.TrimSpace(f) == "" {
			continue
		}
		p, err := ParseProxy(f)
		if err != nil {
			return err
		}
		out = append(out, p)
	}
	*ps = out
	return nil
}

// Resolver resolves client addresses. The zero value trusts no proxy.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a resolver that trusts [DefaultTrusted] and extra.
func New(extra ...Proxy) *Resolver {
	trusted := DefaultTrusted()
	for _, p := range extra {
		trusted = append(trusted, p.Prefix)
	}
	return &Resolver{trusted: trusted}
}

func (r *Resolver) isTrusted(a netip.Addr) bool {
	return slices.ContainsFunc(r.trusted, func(p netip.Prefix) bool { return p.Contains(a) })
}

// Header is a request's header: an http.Header or a connect.Header.
type Header interface {
	// Values returns the values of key, canonicalized.
	Values(key string) []string
}

// Resolve returns the client address of a request that remoteAddr (the
// peer, "host:port" or a bare address) sent with header h, or "" when
// remoteAddr is not an address.
//
// As in ActionDispatch::RemoteIp::GetIp#calculate_ip, the candidates are
// the X-Forwarded-For addresses from the nearest, then the Client-Ip
// ones, and the client is the nearest that is not a trusted proxy; when
// every one is trusted it is the furthest. Unlike Rails, the headers are
// read only when the peer itself is a trusted proxy: a client that
// reaches the server directly cannot claim another address. When both
// headers are set and the Client-Ip address is not among the forwarded
// ones, Rails raises IpSpoofAttackError (remote_ip.rb:151) and fails the
// request; Resolve ignores Client-Ip instead, so that a header cannot
// reject an event.
func (r *Resolver) Resolve(remoteAddr string, h Header) string {
	remote, ok := parseAddr(remoteAddr)
	if !ok {
		if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
			remote, ok = parseAddr(host)
		}
	}
	if !ok {
		return ""
	}
	if !r.isTrusted(remote) {
		return remote.String()
	}
	forwarded := addrs(h.Values("X-Forwarded-For"))
	clients := addrs(h.Values("Client-Ip"))
	slices.Reverse(forwarded)
	slices.Reverse(clients)
	if len(forwarded) > 0 && len(clients) > 0 && !slices.Contains(forwarded, clients[len(clients)-1]) {
		clients = nil
	}
	ips := append(forwarded, clients...)
	for _, a := range ips {
		if !r.isTrusted(a) {
			return a.String()
		}
	}
	if len(ips) > 0 {
		return ips[len(ips)-1].String()
	}
	return remote.String()
}

// addrs returns the valid addresses of comma- or space-separated header
// values, in order (remote_ip.rb ips_from).
func addrs(values []string) []netip.Addr {
	var out []netip.Addr
	for _, v := range values {
		for f := range strings.FieldsFuncSeq(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			if a, ok := parseAddr(f); ok {
				out = append(out, a)
			}
		}
	}
	return out
}

func parseAddr(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}
