package bridge

import (
	"bytes"
	"context"
	"errors"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/stun"
)

// The QUIC listener runs Syncthing's STUN service, which wraps the blocking
// NAT discovery in svcutil.CallWithContext: on a stop the call returns at once
// and the discovery finishes later on its own goroutine. On iOS that is
// routine — a discovery runs for several seconds after every engine start —
// and upstream's discovery closure assigned the caller's err, which the
// returning call was assigning, reading and returning (#152, decision 043).
//
// The test holds a real service's discovery in flight, stops the service,
// waits until Serve has returned, and only then lets the discovery finish.
// The race detector is the oracle, so run it with -race; without it the test
// still checks that a stop does not wait for the discovery.
func TestIssue152_StopDuringStunDiscoveryIsRaceFree(t *testing.T) {
	id := protocol.NewDeviceID([]byte("issue-152"))
	cfg := config.New(id)
	cfg.Options.RawStunServers = []string{"127.0.0.1:3478"}
	wrapper := config.Wrap("", cfg, id, events.NoopLogger)
	if wrapper.Options().IsStunDisabled() {
		t.Fatal("STUN is disabled by the default options; no discovery would run")
	}

	conn := newHeldStunConn()
	svc := stun.New(wrapper, stunNoopSubscriber{}, conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveGoroutine := make(chan string, 1)
	served := make(chan error, 1)
	go func() {
		serveGoroutine <- goroutineID()
		served <- svc.Serve(ctx)
	}()
	serving := <-serveGoroutine

	var discovering string
	select {
	case discovering = <-conn.discovering:
	case <-time.After(10 * time.Second):
		t.Fatal("the STUN service never started a discovery")
	}

	cancel()
	// Wait for Serve by watching the goroutine dump, not by receiving from
	// served: a channel receive would order everything Serve did before
	// everything the discovery does next, and the race detector would no
	// longer see the late write this test exists to catch.
	if !awaitGoroutineExit(serving, 10*time.Second) {
		t.Fatal("Serve did not return after the stop while a discovery was in flight")
	}

	conn.release()
	if !awaitGoroutineExit(discovering, 10*time.Second) {
		t.Fatal("the discovery did not finish after its connection was closed")
	}

	if err := <-served; !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve() = %v, want context.Canceled", err)
	}
}

var errHeldStunConnClosed = errors.New("closed")

// heldStunConn is a STUN connection whose reads block until release, so a
// discovery stays in flight for as long as the test needs. Deadlines are
// ignored on purpose: go-stun's retransmit timeouts would end it early.
type heldStunConn struct {
	discovering chan string // ID of the discovering goroutine, sent on its first write
	held        chan struct{}
	once        sync.Once
}

func newHeldStunConn() *heldStunConn {
	return &heldStunConn{discovering: make(chan string, 1), held: make(chan struct{})}
}

func (c *heldStunConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.once.Do(func() { c.discovering <- goroutineID() })
	return len(p), nil
}

// ReadFrom returns what the QUIC transport returns once it is closed.
func (c *heldStunConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-c.held
	return 0, nil, errHeldStunConnClosed
}

func (c *heldStunConn) release() { close(c.held) }

func (*heldStunConn) Close() error                     { return nil }
func (*heldStunConn) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (*heldStunConn) SetDeadline(time.Time) error      { return nil }
func (*heldStunConn) SetReadDeadline(time.Time) error  { return nil }
func (*heldStunConn) SetWriteDeadline(time.Time) error { return nil }

type stunNoopSubscriber struct{}

func (stunNoopSubscriber) OnNATTypeChanged(stun.NATType)               {}
func (stunNoopSubscriber) OnExternalAddressChanged(*stun.Host, string) {}

// goroutineID returns the calling goroutine's ID from its stack header
// ("goroutine 42 [running]:").
func goroutineID() string {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	return strings.Fields(string(buf[:n]))[1]
}

// awaitGoroutineExit polls the goroutine dump until goroutine id is gone. It
// synchronizes with nothing on purpose — see the #152 test.
func awaitGoroutineExit(id string, timeout time.Duration) bool {
	header := []byte("\ngoroutine " + id + " ")
	buf := make([]byte, 1<<16)
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		n := runtime.Stack(buf, true)
		for n == len(buf) {
			buf = make([]byte, 2*len(buf))
			n = runtime.Stack(buf, true)
		}
		if !bytes.Contains(append([]byte{'\n'}, buf[:n]...), header) {
			return true
		}
	}
	return false
}
