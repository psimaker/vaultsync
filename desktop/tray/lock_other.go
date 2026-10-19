//go:build !unix

package tray

import "errors"

var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running")

func Lock(string) (func(), error) { return func() {}, nil }
