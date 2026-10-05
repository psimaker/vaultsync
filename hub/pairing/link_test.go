package pairing

import (
	"errors"
	"net/url"
	"testing"
)

// The link format is shared with the iOS app's parser (HubPairingLink,
// HubPairingLinkTests): change both or neither (decision 045).
func TestIssue174_LinkFormat(t *testing.T) {
	cases := []struct{ code, hub, want string }{
		{"TULIP-ANCHOR-42", "", "vaultsync://pair?code=TULIP-ANCHOR-42"},
		{"TULIP-ANCHOR-42", "192.168.1.20:8390", "vaultsync://pair?code=TULIP-ANCHOR-42&hub=192.168.1.20%3A8390"},
		{"OTTER-PIANO-07", "[fd00::5]:8390", "vaultsync://pair?code=OTTER-PIANO-07&hub=%5Bfd00%3A%3A5%5D%3A8390"},
	}
	for _, c := range cases {
		got := Link(c.code, c.hub)
		if got != c.want {
			t.Errorf("Link(%q, %q) = %q, want %q", c.code, c.hub, got, c.want)
		}
		u, err := url.Parse(got)
		if err != nil || u.Scheme != LinkScheme || u.Host != LinkHost {
			t.Fatalf("%q does not parse back: %v", got, err)
		}
		if u.Query().Get("code") != c.code || u.Query().Get("hub") != c.hub {
			t.Errorf("%q round-trips to code=%q hub=%q", got, u.Query().Get("code"), u.Query().Get("hub"))
		}
	}
}

// Mirror table of the Swift floor (HubPairingLink.localHubAddress): both
// layers must decide every row identically.
func TestIssue174_LocalHubAddress(t *testing.T) {
	ok := map[string]string{
		"192.168.1.20:8390":  "192.168.1.20:8390",
		"192.168.1.20":       "192.168.1.20:8390",
		" 10.0.0.7:9000 ":    "10.0.0.7:9000",
		"172.16.4.2:8390":    "172.16.4.2:8390",
		"172.31.255.1:8390":  "172.31.255.1:8390",
		"169.254.3.4:8390":   "169.254.3.4:8390",
		"127.0.0.1:8390":     "127.0.0.1:8390",
		"[fd00::5]:8390":     "[fd00::5]:8390",
		"[fe80::1]":          "[fe80::1]:8390",
		"fd12:3456::1":       "[fd12:3456::1]:8390",
		"[::1]:8390":         "[::1]:8390",
		"::ffff:192.168.1.9": "192.168.1.9:8390",
	}
	for in, want := range ok {
		got, err := LocalHubAddress(in)
		if err != nil || got != want {
			t.Errorf("LocalHubAddress(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	notLocal := []string{
		"8.8.8.8:8390", "203.0.113.9", "172.32.0.1:8390", "100.64.0.1:8390",
		"0.0.0.0:8390", "255.255.255.255:8390", "224.0.0.1:8390",
		"[2001:db8::1]:8390", "nas.local:8390", "hub", "example.com",
	}
	for _, in := range notLocal {
		if _, err := LocalHubAddress(in); !errors.Is(err, ErrNotLocal) {
			t.Errorf("LocalHubAddress(%q) = %v, want ErrNotLocal", in, err)
		}
	}
	bad := []string{
		"", "   ", "192.168.1.20:", ":8390", "192.168.1.20:0", "192.168.1.20:65536",
		"192.168.1.20:08390", "192.168.1.20:http", "http://192.168.1.20:8390",
		"user@192.168.1.20", "192.168.1.20/24", "[fd00::5]:", "[]:8390",
	}
	for _, in := range bad {
		if _, err := LocalHubAddress(in); !errors.Is(err, ErrBadAddress) {
			t.Errorf("LocalHubAddress(%q) = %v, want ErrBadAddress", in, err)
		}
	}
}
