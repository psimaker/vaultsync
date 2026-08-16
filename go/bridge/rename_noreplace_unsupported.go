//go:build !darwin && !ios && !linux

package bridge

func renameNoReplace(_, _ string) error {
	return errNoReplaceUnsupported
}
