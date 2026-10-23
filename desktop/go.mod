module github.com/psimaker/vaultsync/desktop

go 1.26.9

// The pairing client, the Syncthing REST client and the device-side accept
// guard come from the Hub's module (decision 045's pattern, like go/).
replace github.com/psimaker/vaultsync/hub => ../hub

require (
	fyne.io/systray v1.12.2
	github.com/godbus/dbus/v5 v5.1.0
	github.com/psimaker/vaultsync/hub v0.0.0-00010101000000-000000000000
	golang.org/x/sys v0.48.0
)

require filippo.io/edwards25519 v1.2.0 // indirect
