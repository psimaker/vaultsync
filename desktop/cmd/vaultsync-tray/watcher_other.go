//go:build !linux

package main

func missingWatcherHint() (string, error) { return "", nil }
