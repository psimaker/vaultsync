//go:build !windows

package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// The E2E tests hand each process its ports from picks that cannot return
// the same port (#403). A pick that listens on port 0 and closes at once
// leaves the port free for the next pick: on Linux two such picks met
// about once in 6,300 tries, and the engine then could not bind its API
// next to its sync port. A held port stays bound until it is released.
// While it is held, nobody binds it, and no other pick returns it: not
// another hold, not freeLoopbackPort (the pick prepare makes for the
// engine's API port) and not an httptest server like the download servers
// the E2E starts. After the release it is free for the engine.
func TestIssue403_AHeldPortIsNotPickedAgainUntilItIsReleased(t *testing.T) {
	held := map[int]bool{}
	for range 64 {
		port, _ := e2eHoldPort(t)
		if held[port] {
			t.Fatalf("e2eHoldPort returned %d twice", port)
		}
		held[port] = true
	}
	for port := range held {
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			l.Close()
			t.Fatalf("port %d could be bound while it was held", port)
		}
	}
	for range 4096 {
		port, err := freeLoopbackPort()
		if err != nil {
			t.Fatal(err)
		}
		if held[port] {
			t.Fatalf("freeLoopbackPort returned %d, a held port", port)
		}
	}
	for range 64 {
		srv := httptest.NewServer(http.NotFoundHandler())
		port := srv.Listener.Addr().(*net.TCPAddr).Port
		srv.Close()
		if held[port] {
			t.Fatalf("an httptest server listened on %d, a held port", port)
		}
	}

	port, release := e2eHoldPort(t)
	release()
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("port %d cannot be bound after its release: %v", port, err)
	}
	l.Close()
}
