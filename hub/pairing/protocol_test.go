package pairing

import (
	"testing"
)

func TestBoxesAreDirectionAndSessionBound(t *testing.T) {
	key := make([]byte, 32)
	box, err := SealBox("s1", key, DirectionDeviceToHub, map[string]string{"x": "y"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := OpenBox("s1", key, DirectionHubToDevice, box, &out); err == nil {
		t.Fatal("box reflected to the other direction opened")
	}
	if err := OpenBox("s2", key, DirectionDeviceToHub, box, &out); err == nil {
		t.Fatal("box replayed into another session opened")
	}
	other := make([]byte, 32)
	other[0] = 1
	if err := OpenBox("s1", other, DirectionDeviceToHub, box, &out); err == nil {
		t.Fatal("box opened under another key")
	}
	if err := OpenBox("s1", key, DirectionDeviceToHub, box, &out); err != nil || out["x"] != "y" {
		t.Fatalf("legit open: %v %v", err, out)
	}
	raw, _ := B64Decode(box)
	raw[len(raw)-1] ^= 0x01
	if err := OpenBox("s1", key, DirectionDeviceToHub, B64Encode(raw), &out); err == nil {
		t.Fatal("tampered box opened")
	}
	if err := OpenBox("s1", key, DirectionDeviceToHub, B64Encode([]byte{1, 2}), &out); err == nil {
		t.Fatal("short box opened")
	}
}
