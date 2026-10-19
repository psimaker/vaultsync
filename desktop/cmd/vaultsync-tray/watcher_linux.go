//go:build linux

package main

import (
	"github.com/godbus/dbus/v5"
)

// missingWatcherHint names the one thing a GNOME desktop needs for a tray
// icon — once, at start: GNOME ships no StatusNotifierWatcher, so without
// the extension the icon has nowhere to appear. The app keeps running and
// registers as soon as a watcher appears (systray re-registers by itself).
func missingWatcherHint() string {
	conn, err := dbus.SessionBus()
	if err != nil {
		return "no session bus (" + err.Error() + "): the icon cannot appear until there is a desktop session"
	}
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, "org.kde.StatusNotifierWatcher").Store(&has); err != nil || has {
		return ""
	}
	return "no status icon host on this desktop: on GNOME install the “AppIndicator and KStatusNotifierItem Support” extension (https://extensions.gnome.org/extension/615/appindicator-support/) and log in again; the icon appears as soon as a host is there"
}
