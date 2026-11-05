// Package plist reads what VaultSync needs from an XML property list: the
// program a LaunchAgent names. The agent and the menu-bar app read it alike.
package plist

import "encoding/xml"

// FirstString reads the first string of the array under key in the top-level
// dictionary of an XML property list — a LaunchAgent's program, in
// ProgramArguments.
func FirstString(data []byte, key string) (string, bool) {
	strs, ok := Array(data, key)
	if !ok || len(strs) == 0 {
		return "", false
	}
	return strs[0], true
}

// Array reads the strings of the array under key in the top-level dictionary
// of an XML property list.
func Array(data []byte, key string) ([]string, bool) {
	var plist struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Value   string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		return nil, false
	}
	items := plist.Dict.Items
	for i := 0; i+1 < len(items); i++ {
		if items[i].XMLName.Local == "key" && items[i].Value == key {
			if next := items[i+1]; next.XMLName.Local == "array" {
				return next.Strings, true
			}
			return nil, false
		}
	}
	return nil, false
}
