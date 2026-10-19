//go:build !unix

package tray

import "errors"

var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running")

func Lock(string) (func(), bool, error) { return func() {}, true, nil }
