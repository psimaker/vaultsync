//go:build !unix

package tray

import (
	"errors"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running (or VaultSync is being removed right now)")

type noLock struct{}

func (noLock) Valid() bool { return true }
func (noLock) Release()    {}

func TakeLock(layout.Layout) (LockHandle, error) { return noLock{}, nil }
