//go:build !unix

package tray

import "github.com/psimaker/vaultsync/desktop/internal/layout"

type noLock struct{}

func (noLock) Valid() bool { return true }
func (noLock) Release()    {}

func TakeLock(layout.Layout) (LockHandle, error) { return noLock{}, nil }
