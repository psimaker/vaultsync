//go:build unix

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// fileOwner returns the numeric uid:gid owning path, so a permission denial can
// carry the exact `-u uid:gid` fix instead of a generic "match the owner" hint.
// Stat only needs search permission on the parent directories — it works on the
// very file we were just denied read access to. When even the file's stat is
// denied (config dir itself is 0700), the directory's owner is reported
// instead: Syncthing keeps config.xml and its directory under the same user.
func fileOwner(path string) (uid, gid uint32, ok bool) {
	for _, p := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			return st.Uid, st.Gid, true
		}
	}
	return 0, 0, false
}
