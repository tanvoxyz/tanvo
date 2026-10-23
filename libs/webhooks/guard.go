package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
)

// ErrBlockedAddress is returned when a destination resolves to an address
// that webhooks may not reach.
var ErrBlockedAddress = errors.New("webhooks: destination address is not allowed")

// blockedV4 are the IPv4 special-purpose ranges (IANA registry) that are
// not globally reachable.
var blockedV4 = prefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
)

// blockedV6 are the special-purpose ranges inside IPv6 global unicast.
var blockedV6 = prefixes("2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20")

var (
	// globalUnicast is 2000::/3; every other IPv6 address (loopback,
	// IPv4-mapped, unique local, link-local, multicast, …) is blocked.
	globalUnicast = netip.MustParsePrefix("2000::/3")
	// nat64 is the well-known DNS64 prefix; its embedded IPv4 address is
	// what gets checked.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")
)

func prefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Blocked reports whether webhooks may not connect to a: it is not a
// globally reachable unicast address.
func Blocked(a netip.Addr) bool {
	a = a.WithZone("")
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
	}
	ranges := blockedV4
	if a.Is6() {
		if !globalUnicast.Contains(a) {
			return true
		}
		ranges = blockedV6
	}
	for _, p := range ranges {
		if p.Contains(a) {
			return true
		}
	}
	return !a.IsValid()
}

// Resolver resolves host names; *net.Resolver implements it.
type Resolver interface {
	// LookupNetIP returns the addresses of host for network "ip", "ip4" or
	// "ip6".
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Resolve returns the addresses of host (an IP literal resolves to
// itself). It fails with [ErrBlockedAddress] when any address is blocked,
// so that a host cannot mix a public and a private address.
func Resolve(ctx context.Context, r Resolver, host string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		if addrs, err = r.LookupNetIP(ctx, "ip", host); err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("resolve %s: no address", host)
		}
	}
	for _, a := range addrs {
		if Blocked(a) {
			return nil, fmt.Errorf("%s (%s): %w", host, a, ErrBlockedAddress)
		}
	}
	return addrs, nil
}

// guardedDialer connects only to addresses that are not blocked. It
// resolves the host itself and dials the checked addresses, so a DNS
// answer that changes between the check and the connection (rebinding)
// cannot reach a private address; the socket control re-checks the
// address actually connected to.
type guardedDialer struct {
	resolver Resolver
	dialer   *net.Dialer
}

func newGuardedDialer(r Resolver, d *net.Dialer) *guardedDialer {
	d.ControlContext = func(_ context.Context, _, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || Blocked(ap.Addr()) {
			return fmt.Errorf("%s: %w", address, ErrBlockedAddress)
		}
		return nil
	}
	return &guardedDialer{resolver: r, dialer: d}
}

func (g *guardedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}
	addrs, err := Resolve(ctx, g.resolver, host)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, a := range addrs {
		c, err := g.dialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}
