//go:build !unix

package lock

import "errors"

var ErrHeld = errors.New("the lock is held by another process")

type Handle struct{}

var errUnsupported = errors.New("file locks are for macOS and Linux only for now")

func Take(string) (*Handle, error) { return nil, errUnsupported }
func Wait(string) (*Handle, error) { return nil, errUnsupported }
func (*Handle) Valid() bool        { return false }
func (*Handle) Release()           {}
