//go:build windows

package main

// On Windows, vaultsync-hub is built for `pair`, which writes no Hub state;
// the Hub itself runs on Linux. `serve` and `code` built here are no supported
// Hub, so the state file keeps only the in-process lock (stateStore.mu) and
// its writer's owner.

func lockStateFile(string) (func(), error) { return func() {}, nil }

func stateFileOwner(string) (fileOwner, error) { return fileOwner{}, nil }
