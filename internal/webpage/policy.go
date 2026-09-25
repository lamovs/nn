package webpage

import (
	"net/netip"
)

// deniedPrefixes are blocked unless the caller allows private addresses.
var deniedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.0.0.0/24",
		"192.0.2.0/24",
		"192.168.0.0/16",
		"198.18.0.0/15",
		"198.51.100.0/24",
		"203.0.113.0/24",
		"224.0.0.0/4",
		"240.0.0.0/4", // includes 255.255.255.255
		"::/128",
		"::1/128",
		"::ffff:0:0:0/96", // SIIT IPv4-translated
		"64:ff9b:1::/48",  // local-use NAT64, never globally reachable
		"100::/64",        // discard-only
		"2001::/32",       // Teredo, carries an obfuscated IPv4
		"2001:db8::/32",
		"fc00::/7",
		"fe80::/10",
		"fec0::/10",
		"ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	nat64Prefix  = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour    = netip.MustParsePrefix("2002::/16")
	v4Compatible = netip.MustParsePrefix("::/96")
)

// publicAddr runs on the actual socket address of every connection.
func publicAddr(ap netip.AddrPort) bool {
	return publicIP(ap.Addr())
}

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.WithZone("").Unmap()
	for _, p := range deniedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	if v4, ok := embeddedIPv4(ip); ok {
		return publicIP(v4)
	}
	return true
}

// embeddedIPv4 unwraps NAT64, 6to4 and IPv4-compatible addresses.
func embeddedIPv4(ip netip.Addr) (netip.Addr, bool) {
	if !ip.Is6() {
		return netip.Addr{}, false
	}
	b := ip.As16()
	switch {
	case nat64Prefix.Contains(ip), v4Compatible.Contains(ip):
		return netip.AddrFrom4([4]byte(b[12:16])), true
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(b[2:6])), true
	}
	return netip.Addr{}, false
}
