//go:build !unix

package tray

import "errors"

var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running")

type Lock struct{}

func TakeLock(string) (*Lock, error) { return &Lock{}, nil }
func (*Lock) Valid() bool            { return true }
func (*Lock) Release()               {}
