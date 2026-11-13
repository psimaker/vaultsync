package pairing

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
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

// SanitizeName keeps a Hub's or a device's name, and the name typed for a new
// vault, printable and single-line for the wire, for terminals and for a list
// that shows it beside an address, at most 64 bytes. A name comes from anyone
// on the network (a discovery answer) or from the other end of a pairing, so
// it loses what can hide or reorder the text around it (hides). It never
// begins or ends with a space, and a name with nothing in it but spaces and
// joiners is no name: the empty string.
func SanitizeName(name string) string {
	// Trimmed after the dropping, not before: what was dropped may have stood
	// between the name and the spaces around it.
	out := strings.TrimSpace(withoutHidden(name))
	// At most 64 bytes.
	out = strings.TrimSpace(cutTo(out, 64))
	// Joiners draw nothing by themselves, and "no name" is told by the empty
	// string: every caller's fallback hangs on it.
	if drawsNothing(out) {
		return ""
	}
	return out
}

// withoutHidden returns text without the characters that hide. The tags of
// an emoji flag stay where they follow U+1F3F4. Bytes that are no UTF-8 come
// out as U+FFFD, so the result is always valid text.
func withoutHidden(text string) string {
	var b strings.Builder
	flag := false // inside the tags that follow U+1F3F4 in an emoji flag
	for _, r := range text {
		switch {
		case flag && flagTag(r):
			flag = r != cancelTag
		case hides(r):
			flag = false
			continue
		default:
			flag = r == blackFlag
		}
		b.WriteRune(r)
	}
	return b.String()
}

// cutTo returns text cut to at most limit bytes, between two characters: a
// byte cut through a multi-byte character would put invalid UTF-8 on the
// wire and on the terminal.
func cutTo(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

// drawsNothing reports a text of spaces and joiners alone.
func drawsNothing(text string) bool {
	return strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || joiner(r) }) == ""
}

// hides reports a character that can hide or reorder the text around it: a
// control character (C0, DEL and C1; U+009B starts a terminal sequence), a
// format character (U+202E reverses what follows, and a direction mark moves
// what stands beside it) or a line or paragraph separator (U+2028 breaks the
// line). Two kinds of format character only shape what a name is written
// with and stay (decision 061): the joiners, and the tags of an emoji flag,
// which SanitizeName keeps where they follow U+1F3F4.
func hides(r rune) bool {
	if joiner(r) {
		return false
	}
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// joiner: U+200C keeps two letters apart (a Persian plural is written with
// it), U+200D joins two (several emoji are two joined by it, and after some
// letters it chooses the letter's form, also at the end of a word).
func joiner(r rune) bool { return r == 0x200c || r == 0x200d }

// The flags of England, Scotland and Wales are U+1F3F4 followed by tag
// letters and digits and closed by the cancel tag. Tags are format
// characters and draw nothing anywhere else, so they stay only there.
const (
	blackFlag = 0x1f3f4
	cancelTag = 0xe007f
)

func flagTag(r rune) bool {
	return r >= 0xe0030 && r <= 0xe0039 || r >= 0xe0061 && r <= 0xe007a || r == cancelTag
}
