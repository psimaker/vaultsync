package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// #292: a name comes from anyone on the network and is shown beside the
// address a person chooses a Hub by. Decision 061.

// `vaultsync-hub pair` listed each Hub by its name, the address last and in
// brackets. A name can hold anything that looks like an address, and one
// Hebrew or Arabic letter in it drew the address before the name in a
// terminal that follows a text's direction. The row now begins with the
// address, and a left-to-right mark before the name gives it its direction
// (decisions 056 and 061).
func TestIssue292_PairListsEachHubByItsAddressFirst(t *testing.T) {
	hubs := []discoveredHub{
		{Address: "192.168.1.66:8390", Name: "Home  (192.168.1.20:8390)"},
		{Address: "192.168.1.20:8390", Name: "\u05d1\u05d9\u05ea 192.168.1.66"},
		{Address: "192.168.1.21:8390"},
	}
	var out bytes.Buffer
	// Nothing that is no number of the list chooses a Hub; the third answer does.
	i, err := chooseHubOn(strings.NewReader("\n4\n2\n"), &out, hubs)
	if err != nil || i != 1 {
		t.Fatalf("the Hub chosen: %d, %v", i, err)
	}
	const ask = "Which Hub printed your code? "
	want := "\nSeveral Hubs answered:\n" +
		"  1) 192.168.1.66:8390 — \u200eHome  (192.168.1.20:8390)\n" +
		"  2) 192.168.1.20:8390 — \u200e\u05d1\u05d9\u05ea 192.168.1.66\n" +
		"  3) 192.168.1.21:8390 — \u200e(unnamed Hub)\n" +
		ask + ask + ask
	if out.String() != want {
		t.Fatalf("the list:\n%q\nwant:\n%q", out.String(), want)
	}
}

// The name a Hub gives itself in the handshake was printed and given to the
// local Syncthing as it came, control characters included (#292). It now
// arrives cleaned from the pairing client, in the first answer and in every
// later one.
func TestIssue292_PairKnowsTheHubByItsNameWithoutWhatHides(t *testing.T) {
	ctx := context.Background()
	h := newCountingHub(t)
	h.fx.server.hubName = "\u202eHome\u2028 Hub\x1b[2J\u0085"
	client, hello, err := handshakeChosenHub(ctx, []discoveredHub{{Address: h.addr}}, h.fx.code, nil)
	if err != nil || hello.HubName != "Home Hub[2J" {
		t.Fatalf("the Hub's name: %+q, %v", hello.HubName, err)
	}
	reply, err := client.Provision(ctx, 1, fakeDeviceID, "Laptop", "", false)
	if err != nil || reply.HubName != "Home Hub[2J" {
		t.Fatalf("the Hub's name in a later answer: %+q, %v", reply.HubName, err)
	}

	// A name with nothing left of it: the Hub is still called something, and
	// gets a name in the local Syncthing, which fills an empty one with what
	// the peer announces.
	h = newCountingHub(t)
	h.fx.server.hubName = "\u202e\u200b \u200d"
	if _, hello, err = handshakeChosenHub(ctx, []discoveredHub{{Address: h.addr}}, h.fx.code, nil); err != nil || hello.HubName != "VaultSync Hub" {
		t.Fatalf("a Hub whose name cleans to nothing: %+q, %v", hello.HubName, err)
	}
}

// The Hub registers a device under the name the device sends, and shows it in
// `status`: it is cleaned the same way (#292).
func TestIssue292_TheHubRegistersADeviceWithoutWhatHides(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	if _, err := c.Handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	reply, err := c.Provision(ctx, 1, fakeDeviceID, "\u202eLap\u2029top\u009b \u200f", "Notes", true)
	if err != nil || reply.Error != "" {
		t.Fatalf("provision: %+v %v", reply, err)
	}
	fx.fake.mu.Lock()
	name := ""
	for _, d := range fx.fake.devices {
		if d.DeviceID == fakeDeviceID {
			name = d.Name
		}
	}
	fx.fake.mu.Unlock()
	if name != "Laptop" {
		t.Errorf("the device in Syncthing: %+q", name)
	}
	st, err := fx.store.load()
	if err != nil || len(st.Devices) != 1 || st.Devices[0].Name != "Laptop" {
		t.Errorf("the device in the Hub's state: %+v %v", st.Devices, err)
	}
}
