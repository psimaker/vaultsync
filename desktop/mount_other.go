//go:build !linux

package main

// mountPoints: on macOS every mount has its own device number, which the
// removal's file-system walk already compares.
var mountPoints = func() ([]string, error) { return nil, nil }
