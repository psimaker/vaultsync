package main

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestDiscoveryResponderAnswersProbe(t *testing.T) {
	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	// Find a free port for the responder.
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveDiscovery(ctx, port, "Küchen Hub\n", nil) }()

	// The responder starts asynchronously; UDP has no handshake, so resend
	// the probe (preceded by garbage it must ignore) until an answer arrives.
	target := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	buf := make([]byte, 256)
	var n int
	var from net.Addr
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("responder exited early: %v", err)
		default:
		}
		if _, err := probe.WriteTo([]byte("garbage"), target); err != nil {
			t.Fatal(err)
		}
		if _, err := probe.WriteTo([]byte(discoveryProbe), target); err != nil {
			t.Fatal(err)
		}
		_ = probe.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		var err error
		n, from, err = probe.ReadFrom(buf)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no discovery reply within the deadline: %v", err)
		}
	}
	hub, ok := parseDiscoveryReply(string(buf[:n]), from)
	if !ok {
		t.Fatalf("unparseable reply %q", buf[:n])
	}
	if hub.Address != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) || hub.Name != "Küchen Hub" {
		t.Fatalf("reply: %+v", hub)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestParseDiscoveryReplyRejectsGarbage(t *testing.T) {
	from := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 5), Port: 5000}
	for _, msg := range []string{"", "VSHUB1", "VSHUB1 abc name", "VSHUB1 70000 x", "VSHUB2 8390 x"} {
		if _, ok := parseDiscoveryReply(msg, from); ok {
			t.Errorf("accepted %q", msg)
		}
	}
	hub, ok := parseDiscoveryReply("VSHUB1 8390", from)
	if !ok || hub.Address != "10.0.0.5:8390" || hub.Name != "" {
		t.Fatalf("minimal reply: %+v %v", hub, ok)
	}
}
