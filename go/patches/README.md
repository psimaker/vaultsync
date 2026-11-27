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
- `syncthing/003-faster-cold-start-redial.patch` — faster reconnect after an
  app cold start: `minConnectionLoopSleep` 5s→1s (the dial loop's initial
  rampup is otherwise clamped to 5s rounds while the discovery cache is still
  empty) and TCP dial timeout 10s→5s (a stale cached LAN address must fail
  over to the relay path quickly). iOS-specific tuning, not for upstreaming.
- `syncthing/004-enforce-negotiated-connection-count.patch` — after a new
  connection is accounted, close the newest connections (by connection ID,
  identical on both ends) beyond the negotiated count (#185, decision 042).
  Two simultaneous dials pass the accept check on both sides before either
  has counted the other, and upstream never closes the extra; each side then
  promotes a different connection and the peer's index arrives on the one
  without a registered index handler, which closes it ("folder is not
  running"). With the bridge pinning one connection per peer, this keeps it
  at one. Worth reporting upstream.
- `syncthing/005-stun-discovery-cancel-race.patch` — the STUN service runs
  its blocking NAT discovery through `svcutil.CallWithContext`, which returns
  as soon as the context is canceled while the discovery finishes on its own
  goroutine. Upstream's closure assigned the caller's `err` from there while
  the returning call assigned, read and returned it — a data race on every
  engine stop during a discovery (#152, decision 043). The closure now has
  its own error variable. Unfixed upstream as of v2.1.6-rc.4: carry it
  across a bump, and drop it only once upstream changes the closure and
  `TestIssue152_*` stays green under `-race`. Worth reporting upstream.
- `syncthing/006-ignores-only-into-the-folders-own-directory.patch` — the
  engine's `setIgnores` writes `.stignore` only while the folder's
  `CheckPath` passes and returns its error otherwise. Upstream makes a
  missing folder root (`CreateRoot`, a `MkdirAll`) and writes into a
  directory without the marker, which brought a moved or deleted vault's
  folder back at its old place on the next launch (#371, decision 069). The
  bridge refuses the same before it calls the engine; the patch also covers
  a folder taken away between that check and the write, and any writer that
  reaches `SetIgnores` without the bridge. The engine's other caller, its own
  auto-accept of a folder (`handleAutoAccepts` in `lib/model/model.go`),
  makes the folder's directory but not its marker before it writes the
  default patterns, so with the patch an auto-accepted folder never gets
  them; the bridge never enables auto-accept. VaultSync doctrine, not for
  upstreaming; `TestIssue371_TheEnginesOwnWriteMakesNoFolder` fails without
  it.
- `syncthing/007-checkpath-asks-whether-the-directory-is-the-folders-own.patch`
  — `CheckPath`, once it has found the folder's directory and its marker
  entry, asks a check the bridge installs (`config.DirectoryCheck`) whether
  that directory is the one the folder synced in, and returns its error.
  Upstream takes any directory with a `.stfolder` entry for the folder's, so
  a copy put in a vault's own place was scanned as the vault and its
  differences went out as changes (#329, decision 084). Every scan, pull,
  commit of scan results and write of the ignores (patch 006) asks
  `CheckPath` first. With no check installed it answers as upstream. The
  check must not wait for anything the engine holds while it calls
  `CheckPath`; the bridge's takes no lock. VaultSync doctrine, not for
  upstreaming; `TestIssue329_TheEnginesCheckPathAsksTheRecords` fails
  without it.
- `syncthing/010-natpmp-result-after-cancel.patch` — the NAT-PMP device runs
  its blocking requests (`AddPortMapping`, `GetExternalIPv4Address`) through
  `svcutil.CallWithContext` too, and upstream read the request's result after
  the call whatever its error, while a request outliving a canceled call
  could still assign it — a data race on every engine stop during a request
  to a gateway that speaks NAT-PMP (#195, decision 043). The result is now
  read only when the call returned nil, which it does only after the request
  completed. A completed call returns what it returned before; a canceled
  call now returns 0 or `0.0.0.0` with its error even when the answer arrived
  at the same moment, where upstream could return that answer with the
  error. Both callers in `lib/nat` discard the value whenever the call reports
  an error, so the NAT service behaves as before. Unfixed upstream as
  of v2.1.7: carry it across a bump, and drop it only once upstream changes
  both reads and `TestIssue195_*` stays green under `-race`. The test calls
  the unexported device by its linker names and first checks its copy of the
  type against `lib/pmp`'s source, so a bump that changes the type fails it.
  Worth reporting upstream.
- `go-stun/001-nil-safe-host-methods.patch` — nil-safe host methods.

## Before each release

1. Check the upstream advisory pages:
   - https://github.com/syncthing/syncthing/security/advisories
   - https://github.com/ccding/go-stun/security/advisories
   (Use **Watch → Custom → Security alerts** on both repos so they reach you.)
2. If a fix lands, bump the `require` pin in `go/go.mod` (+ `go.sum`),
   run `make patch`, and confirm all patches still apply cleanly.
3. Confirm `govulncheck` (security workflow) is green for `./bridge/...`.
