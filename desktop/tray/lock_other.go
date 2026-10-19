//go:build !unix

package tray

import "errors"

var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running")

type noLock struct{}

func (noLock) Valid() bool { return true }
func (noLock) Release()    {}

func TakeLock(string) (LockHandle, error) { return noLock{}, nil }
