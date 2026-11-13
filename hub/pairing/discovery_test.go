package pairing

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestParseDiscoveryReplyRejectsGarbage(t *testing.T) {
	from := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 5000}
	for _, msg := range []string{"", "VSHUB1", "VSHUB1 abc name", "VSHUB1 70000 x", "VSHUB2 8390 x"} {
		if _, ok := ParseDiscoveryReply(msg, from); ok {
			t.Errorf("accepted %q", msg)
		}
	}
	hub, ok := ParseDiscoveryReply("VSHUB1 8390", from)
	if !ok || hub.Address != "10.0.0.5:8390" || hub.Name != "" {
		t.Fatalf("minimal reply: %+v %v", hub, ok)
	}
}

func TestDiscoverCollectsAnswersAndStopsOnCancel(t *testing.T) {
	responder, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()
	port := responder.LocalAddr().(*net.UDPAddr).Port
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := responder.ReadFrom(buf)
			if err != nil {
				return
			}
			if string(buf[:n]) == DiscoveryProbe {
				_, _ = responder.WriteTo([]byte(DiscoveryPrefix+strconv.Itoa(port)+" Küchen Hub"), addr)
				_, _ = responder.WriteTo([]byte(DiscoveryPrefix+strconv.Itoa(port)+" Küchen Hub"), addr) // duplicate answer
			}
		}
	}()
	target := []net.Addr{&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}}
	hubs, err := discover(context.Background(), target, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs) != 1 || hubs[0].Address != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) || hubs[0].Name != "Küchen Hub" {
		t.Fatalf("hubs: %+v", hubs)
	}

	// A cancelled discovery returns well before its wait.
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err := discover(ctx, target, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancelled discovery took %s", elapsed)
	}
}

func TestIssue174_SanitizeNameCutsOnRuneBoundaries(t *testing.T) {
	// 63 ASCII bytes, then a multi-byte rune across the 64-byte limit: a
	// byte cut would keep part of it.
	for _, tail := range []string{"ä", "€"} {
		got := SanitizeName(strings.Repeat("a", 63) + tail)
		if !utf8.ValidString(got) || got != strings.Repeat("a", 63) {
			t.Fatalf("SanitizeName(63 × a + %q) = %q (%d bytes)", tail, got, len(got))
		}
	}
	if got := SanitizeName("  Küchen\nHub\x7f  "); got != "KüchenHub" {
		t.Fatalf("control characters: %q", got)
	}
	if got := SanitizeName(strings.Repeat("a", 70)); len(got) != 64 {
		t.Fatalf("ASCII cut: %d bytes", len(got))
	}
}

// A name comes from anyone on the network, and it is shown beside the
// address a person chooses a Hub by. It used to keep everything but the C0
// controls and DEL: U+2028 broke the line in a list, U+202E reversed what
// followed, and a C1 control could start a terminal sequence (#292).
func TestIssue292_SanitizeNameDropsWhatHidesOrReorders(t *testing.T) {
	for _, tc := range []struct{ what, in, want string }{
		{"a line separator", "Home\u2028192.168.1.20", "Home192.168.1.20"},
		{"a paragraph separator", "Home\u2029Hub", "HomeHub"},
		{"a right-to-left override", "Home \u202eHub", "Home Hub"},
		{"embeddings, an override and their end", "\u202aHome\u202b \u202dHub\u202c", "Home Hub"},
		{"isolates and their end", "\u2066Home\u2067 \u2068Hub\u2069", "Home Hub"},
		{"direction marks", "Home\u200e \u200fHub\u061c", "Home Hub"},
		{"C1 controls", "Home\u0080\u0085 \u009bHub\u009f", "Home Hub"},
		{"a byte order mark, a soft hyphen and a word joiner", "\ufeffHo\u00adme\u2060", "Home"},
		{"tag characters", "Home\U000e0001\U000e0041\U000e007f", "Home"},
		{"C0 controls and DEL, as before", "Ho\x1b[2Jme\x09\x7f", "Ho[2Jme"},
		{"spaces that what was dropped leaves at the ends", "\u202e  Home \u2028", "Home"},
		{"nothing but what hides", "\u202e\u2028\u200e\u0085\ufeff", ""},
		{"nothing but spaces and what hides", " \u202e \u2029 ", ""},
	} {
		if got := SanitizeName(tc.in); got != tc.want {
			t.Errorf("%s: SanitizeName(%+q) = %+q, want %+q", tc.what, tc.in, got, tc.want)
		}
	}
	// What is dropped does not count against the 64 bytes, and the cut still
	// falls on a rune boundary.
	if got := SanitizeName(strings.Repeat("\u202e", 30) + strings.Repeat("a", 63) + "\u2028ä"); got != strings.Repeat("a", 63) {
		t.Errorf("cut after dropping: %+q (%d bytes)", got, len(got))
	}
	// Nor do the spaces that stand at the start once a mark before them is
	// dropped: they go before the cut, not after it.
	if got := SanitizeName("\u202e" + strings.Repeat(" ", 10) + strings.Repeat("a", 64)); got != strings.Repeat("a", 64) {
		t.Errorf("cut after trimming: %+q (%d bytes)", got, len(got))
	}
}

// The same function cleans the names people give their own devices and
// vaults. What only shapes letters stays: the two joiners (a Persian plural
// is written with U+200C, and one emoji can be two joined by U+200D), marks
// that combine, and every script's letters (#292).
func TestIssue292_SanitizeNameKeepsWhatANameIsWrittenWith(t *testing.T) {
	for _, name := range []string{
		"Küchen-Hub",
		"Büro 2",
		"书房",
		"\u05d1\u05d9\u05ea",                   // Hebrew
		"\u0627\u0644\u0645\u0643\u062a\u0628", // Arabic
		"\u06cc\u0627\u062f\u062f\u0627\u0634\u062a\u200c\u0647\u0627", // Persian, with U+200C before the plural
		"👩\u200d💻 Work",
		"❤\ufe0f Notes",
		"Cafe\u0301",
		"Home\u00a0Hub",
	} {
		if got := SanitizeName(name); got != name {
			t.Errorf("SanitizeName(%+q) = %+q, want it unchanged", name, got)
		}
	}
	// At an end a joiner has nothing to join, and a name of joiners alone
	// would show as nothing where the empty string says "no name".
	for _, tc := range []struct{ what, in, want string }{
		{"joiners at the ends", "\u200d Home\u200c\u200d", "Home"},
		{"nothing but joiners and spaces", "\u200d \u200c", ""},
		{"a joiner the cut to 64 bytes leaves at the end", strings.Repeat("a", 57) + "👩\u200d💻", strings.Repeat("a", 57) + "👩"},
		{"a space the cut to 64 bytes leaves at the end", strings.Repeat("a", 63) + " b", strings.Repeat("a", 63)},
	} {
		if got := SanitizeName(tc.in); got != tc.want {
			t.Errorf("%s: SanitizeName(%+q) = %+q, want %+q", tc.what, tc.in, got, tc.want)
		}
	}
}

// The way a name arrives from the network: a discovery answer (#292).
func TestIssue292_ADiscoveredHubsNameArrivesWithoutWhatHides(t *testing.T) {
	from := &net.UDPAddr{IP: net.IPv4(192, 168, 1, 66), Port: 5000}
	hub, ok := ParseDiscoveryReply("VSHUB1 8390 \u202eHome\u2028192.168.1.20\u0085", from)
	if !ok || hub.Address != "192.168.1.66:8390" || hub.Name != "Home192.168.1.20" {
		t.Fatalf("the answer: %+q at %s (%v)", hub.Name, hub.Address, ok)
	}
}
