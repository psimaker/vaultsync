package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// LAN discovery: a device broadcasts "VSHUB1?" on UDP, every Hub answers with
// "VSHUB1 <port> <name>". Deliberately no mDNS dependency and no trust: the
// answer only tells a device where to *try* the PAKE handshake, and only the
// Hub holding the code can complete it. Pairing is restricted to the LAN on
// purpose — UDP broadcast does not route, and the pairing port must never be
// forwarded (decision 036).

const (
	discoveryProbe  = "VSHUB1?"
	discoveryPrefix = "VSHUB1 "
)

// discoveredHub is one answer to a discovery probe.
type discoveredHub struct {
	Address string // host:port of the pairing HTTP service
	Name    string
}

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
	reply := []byte(discoveryPrefix + strconv.Itoa(port) + " " + sanitizeName(hubName))
	buf := make([]byte, 64)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if strings.TrimSpace(string(buf[:n])) != discoveryProbe {
			continue
		}
		if _, err := conn.WriteTo(reply, addr); err != nil && logf != nil {
			logf("discovery: reply failed: %v", err)
		}
	}
}

// discoverHubs broadcasts a probe and collects every answer within wait.
func discoverHubs(ctx context.Context, port int, wait time.Duration) ([]discoveredHub, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	targets := broadcastTargets(port)
	sent := 0
	for _, t := range targets {
		if _, err := conn.WriteTo([]byte(discoveryProbe), t); err == nil {
			sent++
		}
	}
	if sent == 0 {
		return nil, errors.New("could not send a discovery probe on any network interface")
	}

	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	seen := map[string]bool{}
	var hubs []discoveredHub
	buf := make([]byte, 256)
	for {
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			break // deadline reached (or ctx cancelled): return what we have
		}
		hub, ok := parseDiscoveryReply(string(buf[:n]), addr)
		if !ok || seen[hub.Address] {
			continue
		}
		seen[hub.Address] = true
		hubs = append(hubs, hub)
	}
	return hubs, nil
}

func parseDiscoveryReply(msg string, from net.Addr) (discoveredHub, bool) {
	if !strings.HasPrefix(msg, discoveryPrefix) {
		return discoveredHub{}, false
	}
	fields := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(msg, discoveryPrefix)), " ", 2)
	port, err := strconv.Atoi(fields[0])
	if err != nil || port < 1 || port > 65535 {
		return discoveredHub{}, false
	}
	udp, ok := from.(*net.UDPAddr)
	if !ok {
		return discoveredHub{}, false
	}
	name := ""
	if len(fields) == 2 {
		name = sanitizeName(fields[1])
	}
	return discoveredHub{Address: net.JoinHostPort(udp.IP.String(), strconv.Itoa(port)), Name: name}, true
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

// sanitizeName keeps a hub name printable and single-line for the wire and
// for terminals (a hub name is operator-chosen, never user data from a peer).
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
