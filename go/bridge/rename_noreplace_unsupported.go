//go:build !darwin && !ios && !linux

package bridge

func renameNoReplaceAt(_ int, _, _ string) error {
	return errNoReplaceUnsupported
}
