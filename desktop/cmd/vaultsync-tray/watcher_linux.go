//go:build linux

package main

import (
	"errors"

	"github.com/godbus/dbus/v5"
)

// missingWatcherHint names the one thing a GNOME desktop needs for a tray
// icon — once, at start: GNOME ships no StatusNotifierWatcher, so without
// the extension the icon has nowhere to appear. The app keeps running and
// registers as soon as a watcher appears (systray re-registers by itself).
// Without a session bus at all there is no tray for this process — the
// library connects once and never again — so that is an error, not a hint.
func missingWatcherHint() (string, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return "", errors.New("no session bus (" + err.Error() + "): start vaultsync-tray from your desktop session")
	}
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has); err != nil || has {
		return "", nil
	}
	return "no status icon host on this desktop: on GNOME install the “AppIndicator and KStatusNotifierItem Support” extension (https://extensions.gnome.org/extension/615/appindicator-support/) and log in again; the icon appears as soon as a host is there", nil
}
