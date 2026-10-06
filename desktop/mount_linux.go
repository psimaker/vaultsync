package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// mountPoints lists the mount points of this process's view of the file
// systems (/proc/self/mountinfo). A bind mount keeps the device number of the
// file system it comes from, so only this list shows it.
var mountPoints = func() ([]string, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 {
			out = append(out, unescapeMountinfo(fields[4]))
		}
	}
	return out, sc.Err()
}

// unescapeMountinfo decodes the octal escapes the kernel writes for spaces,
// tabs, newlines and backslashes (\040 …).
func unescapeMountinfo(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
