# Vendored fork patches & upstream security tracking

VaultSync embeds a **patched copy** of two upstream Go modules. `make patch`
(`../vendor-patch.sh`) regenerates them from the module cache and re-applies the
patches below; the directories themselves (`go/_syncthing_patched`,
`go/_go-stun_patched`) are gitignored and `replace`-shadowed in `go/go.mod`.

## Why this needs manual attention

Because the forks are gitignored and replaced by local directories, they are
**invisible to graph-based scanners** (Dependabot, GitHub's dependency-graph
advisory alerts). A CVE fixed upstream will **not** surface here automatically.
Two safety nets compensate:

- **`govulncheck`** (`.github/workflows/security.yml`) runs *after* `make patch`,
  so it scans the patched source actually compiled into the app — the one
  automated tool that sees inside the fork.
- **This watch process** for advisories that haven't yet reached the Go vuln DB.

## Pinned upstream

| Module                          | Pin                                                       | Notes                          |
| ------------------------------- | -------------------------------------------------------- | ------------------------------ |
| `github.com/syncthing/syncthing` | `v1.30.0-rc.1.0.20260211104138-dc2a77ab8e5b` (commit `dc2a77ab8e5b`, 2026-02-11) | Pseudo-version of a 2.x build |
| `github.com/ccding/go-stun`      | `v0.1.5`                                                  |                                |

> The Syncthing pseudo-version reads like `v1.30.0-rc…` but the vendored module
> is upstream **2.x** (see `_syncthing_patched/relnotes/v2.0.md`). Both modules
> are `ignore`d in `.github/dependabot.yml` so Dependabot doesn't churn on them.

## Patches

- `syncthing/001-relay-client-nil-url.patch` — nil-URL guard in the relay client
  (crash fix). **Security-relevant**: re-validate against Syncthing relay
  advisories on each bump.
- `syncthing/002-api-auto-noassets.patch` — build with `-tags noassets`.
- `go-stun/001-nil-safe-host-methods.patch` — nil-safe host methods.

## Before each release

1. Check the upstream advisory pages:
   - https://github.com/syncthing/syncthing/security/advisories
   - https://github.com/ccding/go-stun/security/advisories
   (Use **Watch → Custom → Security alerts** on both repos so they reach you.)
2. If a fix lands, bump the `require` pin in `go/go.mod` (+ `go.sum`),
   run `make patch`, and confirm the three patches still apply cleanly.
3. Confirm `govulncheck` (security workflow) is green for `./bridge/...`.
