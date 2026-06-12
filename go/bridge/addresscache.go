// Last-known-good address caching.
//
// After a cold start the discovery cache is empty, so the first connection
// attempt has to wait for a global-discovery round trip before it can even
// dial. To skip that wait, remember the address of every successful outbound
// connection in the device's config (alongside "dynamic"), so the very first
// dial of the next session goes straight to where the peer was last reachable.
//
// Only devices whose address list is still the default ("dynamic") — or one we
// cached ourselves earlier — are touched; user-managed static addresses are
// left alone.
package bridge

import (
	"strings"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
)

const dynamicAddr = "dynamic"

// startAddressCache subscribes to DeviceConnected events and persists the
// dialed address into the device's config. Runs until ctx is canceled
// (StopSyncthing cancels the early-supervisor context).
func startAddressCache(ctx contextDone, cfg config.Wrapper, evLogger events.Logger) {
	sub := evLogger.Subscribe(events.DeviceConnected)
	go func() {
		defer sub.Unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-sub.C():
				data, ok := ev.Data.(map[string]string)
				if !ok {
					continue
				}
				uri, ok := dialableURI(data["type"], data["addr"])
				if !ok {
					continue
				}
				cacheDeviceAddress(cfg, data["id"], uri)
			}
		}
	}()
}

// contextDone is the minimal context surface the cache loop needs; it keeps
// the goroutine testable without a full context.Context.
type contextDone interface {
	Done() <-chan struct{}
}

// dialableURI converts a DeviceConnected event's connection type and remote
// address into a dial URI. Only outbound ("-client") TCP/QUIC connections
// qualify: inbound remote addresses carry the peer's ephemeral source port,
// and relay addresses lack the relay-ID query the dialer needs. Link-local
// IPv6 zones ("%en0") are skipped — the zone would need URI escaping and is
// not meaningfully redialable.
func dialableURI(connType, addr string) (string, bool) {
	if addr == "" || strings.Contains(addr, "%") {
		return "", false
	}
	switch connType {
	case "tcp-client":
		return "tcp://" + addr, true
	case "quic-client":
		return "quic://" + addr, true
	default:
		return "", false
	}
}

// updatedAddresses returns the new address list for a device after caching
// uri, and whether anything changed. Lists not managed by us (anything other
// than the default ["dynamic"] or a previous [cached, "dynamic"]) are
// returned unchanged.
func updatedAddresses(current []string, uri string) ([]string, bool) {
	switch {
	case len(current) == 0,
		len(current) == 1 && current[0] == dynamicAddr:
		return []string{uri, dynamicAddr}, true
	case len(current) == 2 && current[1] == dynamicAddr && current[0] != dynamicAddr:
		if current[0] == uri {
			return current, false
		}
		return []string{uri, dynamicAddr}, true
	default:
		return current, false
	}
}

func cacheDeviceAddress(cfg config.Wrapper, deviceID, uri string) {
	id, err := protocol.DeviceIDFromString(deviceID)
	if err != nil {
		return
	}

	// Cheap pre-check outside Modify so the common no-change case does not
	// trigger a config commit/save.
	dev, ok := cfg.Device(id)
	if !ok {
		return
	}
	if _, changed := updatedAddresses(dev.Addresses, uri); !changed {
		return
	}

	waiter, err := cfg.Modify(func(c *config.Configuration) {
		for i := range c.Devices {
			if c.Devices[i].DeviceID != id {
				continue
			}
			if next, changed := updatedAddresses(c.Devices[i].Addresses, uri); changed {
				c.Devices[i].Addresses = next
			}
			return
		}
	})
	if err != nil {
		return
	}
	waiter.Wait()
}
