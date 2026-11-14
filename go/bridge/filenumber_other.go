//go:build !unix

package bridge

import "errors"

// fileNumber has no answer here: nothing is recorded, and the marker decides
// alone, as it did before there were records (#315).
func fileNumber(string) (uint64, error) {
	return 0, errors.New("no file numbers on this platform")
}
