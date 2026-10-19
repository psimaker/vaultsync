//go:build !unix

package main

import "os"

func socketFallbackDir(string, func(string) string, int, func(string) (os.FileInfo, error)) string {
	return ""
}
