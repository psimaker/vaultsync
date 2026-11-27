//go:build !unix

package bridge

// fileNumber has no answer here: nothing is recorded, and the marker decides
// alone, as it did before there were records (#315).
func fileNumber(string) (uint64, error) {
	return 0, errNoFileNumbers
}

// markerNumbers has no answer here either (#329).
func markerNumbers(string) (markerReading, error) {
	return markerReading{}, errNoFileNumbers
}
