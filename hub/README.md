# vaultsync-hub — the VaultSync Hub coordinator

A **Hub** is a self-hosted Syncthing instance that keeps every vault and pairs
Obsidian devices with a short code instead of device IDs, folder dialogs and
Web UIs. This module is the coordinator that runs next to that Syncthing.

```
curl -fsSL https://vaultsync.eu/setup.sh | sh      # → 2) Hub
```

User documentation: [`docs/hub.md`](../docs/hub.md). Design records:
[036 pairing](../docs/decisions/036-hub-pairing-lan-code-pake.md),
[037 storage & configuration rules](../docs/decisions/037-hub-storage-layout-and-config-rules.md).

## What is in here

| Path | Purpose |
|---|---|
| `main.go` | CLI: `init`, `serve`, `code`, `status`, `vault …` (Hub) and `pair` (device) |
| `pake/` | SPAKE2 over edwards25519 (RFC 9382) — the pairing code becomes a session key |
| `pairing.go` / `pairclient.go` | pairing protocol v1 (start / finish / provision) — server and device side |
| `discovery.go` | LAN discovery by UDP broadcast (`VSHUB1?` → `VSHUB1 <port> <name>`) |
| `provision.go` | guarded Syncthing changes: slugged vault paths under one root, overlap check, pairing refuses non-empty directories (only the operator's `vault adopt` may take one over), never PATCH a folder |
| `syncthing.go` | minimal REST client + `config.xml` API-key discovery |
| `state.go` | Hub state file (pairing scalar, paired devices), atomic 0600 |
| `docker-compose.yml` | the stack `setup.sh` installs (Syncthing + hub + notify), bind mounts only |
| `scripts/setup.sh` | the one link: menu `1) Obsidian device / 2) Hub` |

## Run the tests

```sh
cd hub
go test ./... -count=1 -race
go vet ./...

# End-to-end with two real Syncthing instances (loopback only; ~15 s):
(cd ../go && make patch && cd _syncthing_patched && go build -tags noassets -o /tmp/st ./cmd/syncthing)
VAULTSYNC_HUB_SYNCTHING_BIN=/tmp/st go test . -run '^TestE2E' -count=1 -v

# Installer
shellcheck -s sh scripts/setup.sh scripts/tests/setup-dry-run-test.sh
scripts/tests/setup-dry-run-test.sh
```

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `SYNCTHING_CONFIG` | probed | path to Syncthing's `config.xml` (API key is read from it) |
| `SYNCTHING_API_URL` | from `config.xml` | override the API address (the container uses the published port) |
| `VAULTSYNC_HUB_STATE` | `/var/lib/vaultsync-hub/state.json` | Hub state file |
| `VAULTSYNC_HUB_VAULTS` | `/var/syncthing/vaults` | vaults root as Syncthing sees it |
| `VAULTSYNC_HUB_VAULTS_LOCAL` | same | vaults root as the hub process sees it |
| `VAULTSYNC_HUB_PORT` | `8390` | pairing (TCP) and discovery (UDP) port; bound on all interfaces, non-private source addresses are refused — never forward it |
| `VAULTSYNC_HUB_NAME` | `VaultSync Hub` | how the Hub introduces itself |

## Release

Tag `hub-vX.Y.Z` on `main`. `hub-docker.yml` publishes
`ghcr.io/psimaker/vaultsync-hub:X.Y.Z` (+ `latest`) and a GitHub release with
`vaultsync-hub_<os>_<arch>` binaries and `SHA256SUMS` — the asset names
`setup.sh` expects. Bump the image tag in `docker-compose.yml` and re-embed it
into `setup.sh` (the dry-run test enforces that both are identical).
