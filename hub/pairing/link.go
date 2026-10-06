package pairing

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// The pairing link a Hub prints as a QR code next to its code (decision 045):
//
//	vaultsync://pair?code=TULIP-ANCHOR-42&hub=192.168.1.20%3A8390
//
// code is the canonical code; hub is optional and, when present, the IP
// literal and port of the Hub's pairing service — a device that has it skips
// discovery. The query is form-encoded (url.Values), so ':' and IPv6 brackets
// travel percent-encoded. The iPhone camera opens the link in VaultSync, which
// only prefills its Add Hub sheet: a link never pairs on its own.

const (
	LinkScheme = "vaultsync"
	LinkHost   = "pair"
)

// Link builds the pairing link for a canonical code and an optional Hub
// address (host:port, empty to leave discovery to the device).
func Link(code, hubAddress string) string {
	q := url.Values{"code": {code}}
	if hubAddress != "" {
		q.Set("hub", hubAddress)
	}
	return LinkScheme + "://" + LinkHost + "?" + q.Encode()
}

var (
	// ErrNotLocal means a Hub address is not a private, loopback or
	// link-local IP literal.
	ErrNotLocal = errors.New("the hub address is not on the local network")
	// ErrBadAddress means a Hub address is not host[:port] at all.
	ErrBadAddress = errors.New("the hub address is malformed")
)

// LocalHubAddress validates a Hub address that came from outside the device —
// a scanned QR code or an opened link — and returns it as ip:port. The host
// must be an IP literal in a private, loopback or link-local range: the same
// rule the Hub's pairing service applies to its callers, so a link can never
// send the handshake to the Internet (decision 036 keeps pairing on the LAN).
// Host names are refused (no DNS on this path); a missing port means
// DefaultPort. The `pair` command's --hub flag deliberately does not use
// this: an operator may name a host on their own network.
func LocalHubAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrBadAddress
	}
	host, port := raw, strconv.Itoa(DefaultPort)
	if h, p, err := net.SplitHostPort(raw); err == nil {
		host, port = h, p
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = raw[1 : len(raw)-1] // bracketed IPv6 without a port
	} else if strings.Count(raw, ":") == 1 {
		return "", ErrBadAddress // "host:" or ":port" — SplitHostPort refused it
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return "", ErrBadAddress
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if host == "" || strings.ContainsAny(host, "/?#@ ") {
			return "", ErrBadAddress
		}
		return "", ErrNotLocal
	}
	if !isLocalIP(ip) {
		return "", ErrNotLocal
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(n)), nil
}

// isLocalIP is the Hub's own rule for its callers (localNetworkOnly in the
// vaultsync-hub command), applied by the device to the Hub.
func isLocalIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}
