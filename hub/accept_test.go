package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// acceptShareLocally is the device-side merge guard: the only way two
// non-empty vaults could be joined by VaultSync itself. These tests pin the
// fail-closed behaviour for every file-count the Hub can report.
func TestAcceptShareLocallyMergeGuard(t *testing.T) {
	ctx := context.Background()
	newLocal := func(t *testing.T, nonEmpty bool) (*fakeSyncthing, *SyncthingClient, *provisioner, string) {
		fake, srv := newFakeSyncthing(t, fakeDeviceID)
		client := fake.client(srv)
		prov := newTestProvisioner(t, fake, client)
		dir := filepath.Join(t.TempDir(), "Notes")
		if nonEmpty {
			testDirs[prov][dir] = true
		}
		fake.pending["vs-aaaaaaaaaaaa"] = []string{fakeHubID}
		return fake, client, prov, dir
	}
	vault := func(files int64) vaultInfo { return vaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Notes", Files: files} }

	t.Run("non-empty local and non-empty hub is refused", func(t *testing.T) {
		fake, client, prov, dir := newLocal(t, true)
		err := acceptShareLocally(ctx, client, prov, fakeHubID, fakeDeviceID, vault(12), dir)
		if err == nil || !strings.Contains(err.Error(), "never merges") {
			t.Fatalf("expected merge refusal, got %v", err)
		}
		if len(fake.folders) != 0 {
			t.Fatal("folder was added despite refusal")
		}
	})
	t.Run("non-empty local and unknown hub count fails closed", func(t *testing.T) {
		fake, client, prov, dir := newLocal(t, true)
		err := acceptShareLocally(ctx, client, prov, fakeHubID, fakeDeviceID, vault(-1), dir)
		if err == nil || !strings.Contains(err.Error(), "could not confirm") {
			t.Fatalf("expected fail-closed refusal, got %v", err)
		}
		if len(fake.folders) != 0 {
			t.Fatal("folder was added despite unknown hub state")
		}
	})
	t.Run("non-empty local and brand-new hub vault is accepted", func(t *testing.T) {
		fake, client, prov, dir := newLocal(t, true)
		if err := acceptShareLocally(ctx, client, prov, fakeHubID, fakeDeviceID, vault(0), dir); err != nil {
			t.Fatal(err)
		}
		if len(fake.folders) != 1 || fake.folders[0].Path != dir || len(fake.folders[0].Devices) != 2 {
			t.Fatalf("folder not configured as expected: %+v", fake.folders)
		}
	})
	t.Run("empty local directory accepts a populated hub vault", func(t *testing.T) {
		fake, client, prov, dir := newLocal(t, false)
		if err := acceptShareLocally(ctx, client, prov, fakeHubID, fakeDeviceID, vault(500), dir); err != nil {
			t.Fatal(err)
		}
		if len(fake.folders) != 1 {
			t.Fatalf("folder not added: %+v", fake.folders)
		}
		if _, created := testDirs[prov][dir]; !created {
			t.Fatal("directory was not created")
		}
	})
	t.Run("overlap with an existing local folder is refused", func(t *testing.T) {
		fake, client, prov, dir := newLocal(t, false)
		fake.folders = append(fake.folders, folderConfig{ID: "other", Label: "Everything", Path: filepath.Dir(dir)})
		err := acceptShareLocally(ctx, client, prov, fakeHubID, fakeDeviceID, vault(0), dir)
		if err == nil || !strings.Contains(err.Error(), "overlaps") {
			t.Fatalf("expected overlap refusal, got %v", err)
		}
	})
}
