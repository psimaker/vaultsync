package pairing

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
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
