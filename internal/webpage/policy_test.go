package webpage

import (
	"net/netip"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	denied := []string{
		"0.0.0.0", "0.1.2.3", "10.0.0.1", "100.64.0.1", "100.127.255.254",
		"127.0.0.1", "127.8.9.10", "169.254.169.254", "169.254.0.1",
		"172.16.0.1", "172.31.255.255", "192.0.0.8", "192.168.1.1",
		"198.18.0.1", "198.19.255.255", "224.0.0.1", "239.255.255.250",
		"240.0.0.1", "255.255.255.255",
		"::", "::1", "fe80::1", "fe80::1%en0", "fc00::1", "fd12:3456::1",
		"fec0::1", "ff02::1", "2001:db8::1",
		"::ffff:127.0.0.1", "::ffff:10.1.2.3", "::ffff:169.254.169.254",
		"64:ff9b::7f00:1", "64:ff9b::a00:1", "64:ff9b::a9fe:a9fe",
		"64:ff9b:1::7f00:1", "64:ff9b:1::808:808",
		"2002:7f00:1::1", "2002:c0a8:101::", "2002:a00:1::5",
		"192.0.2.1", "198.51.100.7", "203.0.113.9",
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001::1",
		"100::1", "100::ffff:1", "::ffff:0:808:808", "::ffff:0:7f00:1",
		"::7f00:1", "::a00:1", "::c0a8:101", "::2",
	}
	for _, s := range denied {
		ap := netip.AddrPortFrom(netip.MustParseAddr(s), 443)
		if publicAddr(ap) {
			t.Errorf("publicAddr(%s) = true, want denied", s)
		}
	}

	allowed := []string{
		"8.8.8.8", "1.1.1.1", "93.184.216.34", "100.63.255.255", "100.128.0.1",
		"172.15.255.255", "172.32.0.1", "192.0.1.1", "198.17.255.255",
		"198.20.0.1", "223.255.255.255", "11.0.0.1",
		"2606:4700:4700::1111", "2001:4860:4860::8888", "2a00:1450::1",
		"::ffff:8.8.8.8", "64:ff9b::808:808", "2002:808:808::1",
		"::808:808",
	}
	for _, s := range allowed {
		ap := netip.AddrPortFrom(netip.MustParseAddr(s), 80)
		if !publicAddr(ap) {
			t.Errorf("publicAddr(%s) = false, want allowed", s)
		}
	}

	if publicAddr(netip.AddrPort{}) {
		t.Error("invalid address allowed")
	}
}

func TestControlPolicy(t *testing.T) {
	strict := &Fetcher{}
	open := &Fetcher{AllowPrivate: true}
	for _, addr := range []string{"127.0.0.1:80", "[::1]:443", "[::ffff:127.0.0.1]:80", "10.0.0.1:8080", "[fe80::1%lo0]:80", "not-an-address"} {
		if err := strict.control("tcp", addr, nil); err != ErrBlockedAddress {
			t.Errorf("control(%s) = %v, want ErrBlockedAddress", addr, err)
		}
		if err := open.control("tcp", addr, nil); err != nil {
			t.Errorf("AllowPrivate control(%s) = %v", addr, err)
		}
	}
	if err := strict.control("tcp4", "8.8.8.8:443", nil); err != nil {
		t.Errorf("public address blocked: %v", err)
	}

	seam := &Fetcher{public: func(ap netip.AddrPort) bool { return ap.Port() == 1234 }}
	if err := seam.control("tcp4", "127.0.0.1:1234", nil); err != nil {
		t.Errorf("classifier seam ignored: %v", err)
	}
	if err := seam.control("tcp4", "127.0.0.1:1235", nil); err != ErrBlockedAddress {
		t.Errorf("classifier seam: %v", err)
	}
}
