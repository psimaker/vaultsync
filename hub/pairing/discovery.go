package pairing

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// LAN discovery: a device broadcasts "VSHUB1?" on UDP, every Hub answers with
// "VSHUB1 <port> <name>". Deliberately no mDNS dependency and no trust: the
// answer only tells a device where to *try* the PAKE handshake, and only the
// Hub holding the code can complete it. Pairing is restricted to the LAN on
// purpose — UDP broadcast does not route, and the pairing port must never be
// forwarded (decision 036). The responder lives in the vaultsync-hub command.

const (
	DiscoveryProbe  = "VSHUB1?"
	DiscoveryPrefix = "VSHUB1 "
)

// DiscoveredHub is one answer to a discovery probe.
type DiscoveredHub struct {
	Address string // host:port of the pairing HTTP service
	Name    string
}

// ErrNoProbeSent means no network interface accepted the broadcast — no
// usable network, or (on iOS) local-network access denied.
var ErrNoProbeSent = errors.New("could not send a discovery probe on any network interface")

// DiscoverHubs broadcasts a probe and collects every answer within wait.
// The answers are hints from anyone on the network: a caller that dials one
// on behalf of a user applies LocalHubAddress / NewLocalClient to it.
func DiscoverHubs(ctx context.Context, port int, wait time.Duration) ([]DiscoveredHub, error) {
	return discover(ctx, broadcastTargets(port), wait)
}

func discover(ctx context.Context, targets []net.Addr, wait time.Duration) ([]DiscoveredHub, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	sent := 0
	for _, t := range targets {
		if _, err := conn.WriteTo([]byte(DiscoveryProbe), t); err == nil {
			sent++
		}
	}
	if sent == 0 {
		return nil, ErrNoProbeSent
	}

	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	// A cancelled context ends the wait early instead of at the deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	seen := map[string]bool{}
	var hubs []DiscoveredHub
	buf := make([]byte, 256)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			// The deadline (or a cancelled ctx, which moves it to now) ends
			// the wait normally; any other read error is a failure, never
			// "no Hub answered".
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break
			}
			return hubs, err
		}
		hub, ok := ParseDiscoveryReply(string(buf[:n]), addr)
		if !ok || seen[hub.Address] {
			continue
		}
		seen[hub.Address] = true
		hubs = append(hubs, hub)
	}
	return hubs, nil
}

// ParseDiscoveryReply reads one responder answer. The address is the
// sender's IP with the advertised port — never a host the answer names.
func ParseDiscoveryReply(msg string, from net.Addr) (DiscoveredHub, bool) {
	if !strings.HasPrefix(msg, DiscoveryPrefix) {
		return DiscoveredHub{}, false
	}
	fields := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(msg, DiscoveryPrefix)), " ", 2)
	port, err := strconv.Atoi(fields[0])
	if err != nil || port < 1 || port > 65535 {
		return DiscoveredHub{}, false
	}
	udp, ok := from.(*net.UDPAddr)
	if !ok {
		return DiscoveredHub{}, false
	}
	name := ""
	if len(fields) == 2 {
		name = SanitizeName(fields[1])
	}
	return DiscoveredHub{Address: net.JoinHostPort(udp.IP.String(), strconv.Itoa(port)), Name: name}, true
}

// broadcastTargets returns the limited broadcast plus every interface's
// directed broadcast address; some networks drop one but not the other.
func broadcastTargets(port int) []net.Addr {
	targets := []net.Addr{&net.UDPAddr{IP: net.IPv4bcast, Port: port}}
	ifaces, err := net.Interfaces()
	if err != nil {
		return targets
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			mask := ipnet.Mask
			if len(mask) == 16 {
				mask = mask[12:]
			}
			bcast := make(net.IP, 4)
			for i := range 4 {
				bcast[i] = ip[i] | ^mask[i]
			}
			targets = append(targets, &net.UDPAddr{IP: bcast, Port: port})
		}
	}
	return targets
}

// SanitizeName keeps a hub or device name printable and single-line for the
// wire and for terminals, at most 64 bytes.
func SanitizeName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	// At most 64 bytes, cut on a rune boundary: a byte cut through a
	// multi-byte character would put invalid UTF-8 on the wire.
	for len(out) > 64 {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	return out
}
