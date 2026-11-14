//go:build !unix

package bridge

// fileNumber has no answer here: nothing is recorded, and the marker decides
// alone, as it did before there were records (#315).
func fileNumber(string) (uint64, error) {
	return 0, errNoFileNumbers
}
