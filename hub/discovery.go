package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// LAN discovery responder. The probe format and the device side live in the
// pairing package; see its discovery.go for why discovery carries no trust.

type discoveredHub = pairing.DiscoveredHub

// serveDiscovery answers probes until ctx is done. It listens on all IPv4
// interfaces so broadcasts reach it regardless of which LAN the device is on.
func serveDiscovery(ctx context.Context, port int, hubName string, logf func(string, ...any)) error {
	conn, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("discovery listen: %w", err)
	}
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	reply := []byte(pairing.DiscoveryPrefix + strconv.Itoa(port) + " " + sanitizeName(hubName))
	buf := make([]byte, 64)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if strings.TrimSpace(string(buf[:n])) != pairing.DiscoveryProbe {
			continue
		}
		if _, err := conn.WriteTo(reply, addr); err != nil && logf != nil {
			logf("discovery: reply failed: %v", err)
		}
	}
}

// sanitizeName keeps operator- and peer-supplied names printable and
// single-line (pairing.SanitizeName).
func sanitizeName(name string) string { return pairing.SanitizeName(name) }
