package bridge

import (
	"slices"
	"testing"
)

func TestDialableURI(t *testing.T) {
	cases := []struct {
		name     string
		connType string
		addr     string
		want     string
		ok       bool
	}{
		{"tcp client", "tcp-client", "192.168.1.10:22000", "tcp://192.168.1.10:22000", true},
		{"quic client", "quic-client", "203.0.113.7:22000", "quic://203.0.113.7:22000", true},
		{"ipv6 client", "tcp-client", "[2001:db8::1]:22000", "tcp://[2001:db8::1]:22000", true},
		{"inbound tcp has ephemeral source port", "tcp-server", "192.168.1.10:54321", "", false},
		{"inbound quic", "quic-server", "192.168.1.10:54321", "", false},
		{"relay lacks relay-ID query", "relay-client", "198.51.100.4:22067", "", false},
		{"relay server", "relay-server", "198.51.100.4:22067", "", false},
		{"unknown type", "unknown-type", "192.168.1.10:22000", "", false},
		{"empty addr", "tcp-client", "", "", false},
		{"link-local zone is not URI-safe", "tcp-client", "[fe80::1%en0]:22000", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := dialableURI(tc.connType, tc.addr)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("dialableURI(%q, %q) = (%q, %v), want (%q, %v)",
					tc.connType, tc.addr, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestUpdatedAddresses(t *testing.T) {
	uri := "tcp://192.168.1.10:22000"
	other := "tcp://10.0.0.5:22000"

	cases := []struct {
		name    string
		current []string
		want    []string
		changed bool
	}{
		{"empty defaults to cached+dynamic", nil, []string{uri, "dynamic"}, true},
		{"plain dynamic gets cached", []string{"dynamic"}, []string{uri, "dynamic"}, true},
		{"stale cache is replaced", []string{other, "dynamic"}, []string{uri, "dynamic"}, true},
		{"same cache is a no-op", []string{uri, "dynamic"}, []string{uri, "dynamic"}, false},
		{"user static address untouched", []string{"tcp://nas.local:22000"}, []string{"tcp://nas.local:22000"}, false},
		{"user multi-address untouched", []string{other, uri, "dynamic"}, []string{other, uri, "dynamic"}, false},
		{"dynamic-first pair untouched", []string{"dynamic", other}, []string{"dynamic", other}, false},
		{"double dynamic untouched", []string{"dynamic", "dynamic"}, []string{"dynamic", "dynamic"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := updatedAddresses(tc.current, uri)
			if changed != tc.changed || !slices.Equal(got, tc.want) {
				t.Fatalf("updatedAddresses(%v) = (%v, %v), want (%v, %v)",
					tc.current, got, changed, tc.want, tc.changed)
			}
		})
	}
}
