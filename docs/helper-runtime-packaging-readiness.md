# Helper runtime and packaging readiness

**Status:** Implemented in source and locally verified; not yet published or
deployed. VaultSync 2.0 remains NO-GO. No app runtime calls these endpoints, so
upload, download, and roundtrip evidence remain unset.

## Exact scope

The helper can expose the fixed Decision 022–024 paths only when an operator
supplies both `VAULTSYNC_DIAGNOSTICS_CONFIG` and
`VAULTSYNC_DIAGNOSTICS_STATE`. With neither value, every existing installation
keeps its prior Trigger-v1 behavior and creates no diagnostics state, listener,
credential, namespace, trust, mapping, artifact, or Relay request.

The opt-in runtime provides:

- TLS 1.3 only, a stable P-256 SPKI pin, certificate renewal with the same key,
  and mutually authenticated Ed25519 application messages;
- exact fixed POST paths for D022 pairing, D023 enablement/authorization, and
  D024 capability, upload attestation, response authorization, and cleanup;
- strict deterministic CBOR, signature-domain, binding, epoch, nonce, clock,
  replay, body-size, request-rate, and immutable-state validation;
- a mode-0600 local Unix operator socket for a one-time pairing invitation and
  retrieval of a pending signed namespace enablement request;
- explicit local lifecycle commands for app fingerprints, helper-key/TLS-pin
  rotation, and local revocation; lifecycle records are printed only to the
  invoking operator, never logged;
- a read-only Syncthing preflight before capability and every operation call:
  the pinned local Device ID must match both before and after the exact folder
  and expanded-ignore reads, the folder must still exist, be unpaused and
  `sendreceive`, the ignore parser must report no error, and the expanded
  patterns must not exclude any fixed namespace path;
- bounded Syncthing preflight responses: folder and expanded-ignore JSON is
  capped at 1 MiB, with fixed folder/pattern-count and pattern-length ceilings;
  overflow is unsupported rather than an allocation or matcher-amplification
  path;
- an ephemeral deployment binding over the local folder ID, the canonical path
  reported by Syncthing, the fixed mount alias, and the opened namespace
  device/inode. Only its SHA-256 digest enters the runtime environment; the raw
  host path is not written to runtime config, helper state, protocol bodies, or
  logs;
- redirect rejection for Syncthing, Relay, and local operator HTTP clients, so
  credentials and identifiers cannot follow a redirect;
- exact D023 stable installation bindings and append-only helper and
  authorization epoch chains. Proposed credentials cannot serve operations;
  only an exact proposed-state capability query can make a committed D022
  transition terminal, after which a new D023 authorization epoch is required.
  A global helper-key or TLS-pin transition with several active installations
  returns no signed capability success to an early confirmer until every
  required proposed-state query has made the global commit durable; earlier
  callers receive only unavailable and recover by exact retry.
  A namespace-wide helper manifest is appended only after the same Device ID,
  Syncthing folder/ignore, root, and deployment-mount preflight passes for every
  affected namespace; a partial failure remains append-only and reconciles
  forward without changing credentials prematurely.
- one mode-0600 cross-process mutation lock around every remote operation,
  local operator action, namespace preparation, lifecycle reconciliation, and
  admin mutation. A concurrent `docker exec` rotation or revocation therefore
  cannot interleave a credential-state decision with an immutable namespace
  append;
- bounded crash completion without adoption: an exact D023 authorization whose
  immutable file and protected state became durable can be retried byte for
  byte, including after the candidate's wall-clock expiry because that path can
  only confirm an already-created exact record and cannot advance state. If
  namespace creation and its protected root record became durable but
  the local mount configuration did not, an explicit `enable` rerun can resume
  only that registered root after repeating the Device, folder, ignore, parent
  device/inode, layout, root-digest, helper-key, epoch, and signature checks. It
  creates nothing in recovery mode. Missing or conflicting state fails closed,
  and helper/TLS rotation is unavailable while a registered root still lacks
  its exact authorization.

No endpoint accepts a path, folder ID, Device ID, mount alias, or other
identifier in its URL. Network bodies cannot choose a filesystem path. Cloud
Relay, APNs, StoreKit, Syncthing discovery, peer trust, folder sharing, ignore
rules, rescans, and Syncthing configuration remain outside this runtime.

## Supported packaging row

Only the following row is supported for diagnostics:

| Component | Required boundary |
|---|---|
| Host | Standard Linux host explicitly confirmed by the operator; not NAS. |
| Engine | Rootful Docker Engine; rootless Docker is rejected. |
| Folder | One already configured local Syncthing folder selected by exact local ID and canonical host path; remote/NAS/FUSE/desktop-virtualized filesystems are rejected. |
| Installer phase | Temporary read/write bind of that one selected parent, solely after a signed app enablement and explicit operator confirmation. |
| Runtime phase | Exact existing `VaultSync Diagnostics` child bind read/write; parent vault absent. |
| Other mounts | Separate state read/write; runtime config exact owner-only mode `0400`, single-link and read-only; exact `config.xml` file read-only. |
| Container | Read-only root, all Linux capabilities dropped, `no-new-privileges`, fixed small `/tmp`, exact non-root Syncthing-config owner UID/GID; root-owned config is unsupported. |
| Syncthing API | Exact operator-supplied `http://127.0.0.1:<1-65535>` endpoint on the same host; redirects are rejected. |
| Network | Explicit private/loopback/link-local/CGNAT listen IP and non-root port 1024–65535; host networking publishes no Docker port and creates no public default. |
| Image | Resolved to one local immutable Docker content ID before container creation. |

The separate `notify/scripts/diagnostics-docker.sh` command is the only
supported installer. `init`, `pair`, and `enable` are distinct explicit steps.
`deploy` replaces only the helper container and preserves config, credentials,
namespace records, mappings, backups, versions, conflicts, and tombstones.
`stop` removes no persistent data.

Docker named volumes or subpaths (including their backing tree), rootless Docker,
remote Docker daemons/contexts, non-Unix Docker endpoints, Docker Desktop, WSL,
remote/NAS/FUSE filesystems, NAS paths/packages, Linux
systemd binaries, macOS launchd, and Windows
Scheduled Tasks remain diagnostics-unsupported. The ordinary one-line
installer, PowerShell installer, bootstrap script, and Docker Compose do not
set either diagnostics environment variable and do not activate the runtime.

## Explicit operator flow

1. Confirm the host is in the supported row and set the exact image, folder,
   endpoint, Syncthing config, and Relay values.
2. Run `diagnostics-docker.sh init`. This creates only a private config directory
   and separate private state directory, then starts the capable helper with no
   namespace mount.
3. Run `diagnostics-docker.sh pair`. The one-time QR value is emitted only to
   that terminal. Pairing remains pending until every D022 signed step and the
   explicit app fingerprint comparison complete.
4. After the app sends a signed D023 enablement request, run
   `diagnostics-docker.sh enable` with the exact canonical folder host path and
   explicit supported-host confirmation. The command displays the exact
   `VaultSync Diagnostics` path before mutation and additionally requires
   `VAULTSYNC_DIAGNOSTICS_ENABLE_CONFIRMED=1` after the operator accepts that
   path and possible retention in peers, backups, versions, conflicts, and
   tombstones. The one-shot installer rechecks the
   local Syncthing API, `.stfolder`, ignores, path identity, collision state,
   signatures, and bindings before creating the fixed child. It records the
   source directory's device/inode before the Docker bind and verifies that
   identity again inside the one-shot container before any creation.
   If the process stops only after the root and protected root record are
   durable, rerunning this same explicit command resumes that exact root without
   a live in-memory pending request; it neither creates nor adopts a root.
5. The script restarts the runtime with only that exact child mounted. The app
   must validate the synchronized root and send a signed authorization candidate;
   the helper countersigns and creates the immutable authorization.

A second app installation must pair independently and send its own signed D023
enablement and authorization. It may join the already authenticated namespace
only after those steps; the operator does not create or adopt a second namespace,
and no key, trust, or stable installation identity is transferred from the first
installation. Revocation leaves the affected installation's signed immutable
authorization history in place. Later helper-key rotation may append the next
namespace-wide helper manifest for another active installation, but it never
rewrites the revoked history; that active installation must then append its own
fresh D023 authorization epoch before operations resume.

No step discovers a helper, pairs trust, creates a namespace, changes a share,
or adopts existing content automatically.

## Upgrade, downgrade, and rollback

An upgrade or forward recovery resolves the requested image once, runs the
container by immutable content ID, and revalidates state and namespace from
scratch. A downgrade runs the older immutable image against the same preserved
mounts. An older helper ignores the opt-in environment/state it does not know,
so capability becomes unavailable while Trigger v1 remains unchanged. It does
not delete or rewrite credentials, namespace content, mappings, or user data.

Re-upgrading reopens the protected state and exact namespace, checks file and
mount identity, validates the full helper chain plus every immutable installation
chain at the helper epoch to which it was last signed, and never resumes an old
app operation or proof. Runtime use still requires the selected active app's
exact current state digest, keys, epochs, and newest authorization. Helper/app
rollback cannot erase copies retained by
Syncthing peers, versioning, backups, conflict copies, remote history, or
tombstones.

Namespace/root registration, credential-state updates, and immutable-file
creation are append/atomic operations rather than a distributed transaction.
Their narrow acknowledged crash windows therefore recover only forward: exact
registered-root continuation and exact authorization-message retry are allowed;
rollback, replacement, regeneration, or adoption of partial/conflicting content
is not.

## Compatibility matrix

The diagnostics contract is additive and opt-in. Relay v1 and Trigger v1 retain
their existing wire formats.

| App | Helper | Relay | Result |
|---|---|---|---|
| Released/old | Released/old | Existing v1 | Existing Trigger-v1 behavior only. |
| Released/old | New, diagnostics unset | Existing v1 | Byte-compatible Trigger-v1 behavior; no diagnostics listener, state, pairing, or namespace. |
| Released/old | New, diagnostics explicitly enabled | Existing v1 | Trigger v1 remains available; no app calls the fixed diagnostics paths, so capability and all directional evidence remain unset. |
| Future capable | Released/old | Existing v1 | Diagnostics capability is unavailable because no fixed TLS endpoint exists; the app must not create, trust, or transfer anything. |
| Future capable | New, enabled but unpaired/unauthorized | Existing v1 | Explicit unavailable/unsupported; no automatic pairing, trust, namespace creation, or fallback proof. |
| Future capable | New, enabled and exactly authorized | Existing v1 | Helper-side D022–D024 contract is available; Relay v1 is unchanged and is not evidence for upload, download, or roundtrip. |
| Any | New → old rollback → same new image | Existing v1 | Diagnostics becomes unavailable on rollback; preserved state is revalidated on forward recovery and no operation resumes automatically. |

This milestone does not claim compatibility with an unreleased app
implementation. App-side old/new matrices and real-device evidence remain
gates for the later app milestones.

## Evidence boundary

The strongest helper-side proof in this milestone is runtime reconstruction of
an exact authenticated, descriptor-confined D023 authorization followed by an
exact signed D024 capability response. The dormant foundations can also process
their exact messages through the runtime endpoints, but no released app invokes
them and no iPhone has accepted a response after a fresh post-authorization
`ItemFinished`.

| Claim | State after this milestone |
|---|---|
| Authenticated capability | Helper-side runtime and local Linux test evidence only. |
| Upload | Unset in the product; no app-authored runtime request has been accepted as app evidence. |
| Download | Unset; no response has passed a fresh iPhone apply baseline. |
| Roundtrip | Unset; no same-chain upload-then-download correlation exists. |
| Cleanup | Helper-side exact digest cleanup only; evidence-orthogonal. |

Signatures prove authorship and exact causal bindings, not transport route,
direct peer, block provenance, future delivery, or global sync health.

## Verification

The readiness gate includes:

- native full Go tests plus race, vet, formatting, vulnerability, secret, and
  repository policy checks;
- Linux container tests for exact namespace creation, descriptor confinement,
  pre-bind source identity, ephemeral mount binding, mount-swap rejection,
  pinned Device ID before/after Syncthing reads, initial authorization,
  separately paired multi-installation authorization, app revocation with
  immutable historical records, app-key rotation, helper-key/manifest rotation,
  authorization epochs, cross-process mutation serialization, registered-root
  crash continuation, exact post-state authorization retry after signed expiry,
  multi-installation global TLS response gating, and restart reconstruction;
- installer-policy checks that reject WSL, remote/NAS/FUSE storage, Docker
  named-volume backing paths, ambiguous bind-source strings, rootless Docker,
  and non-loopback Syncthing API endpoints before deployment;
- production-image startup with read-only root, dropped capabilities, separate
  state/config, exact mounts, private listener, and fixed privacy-safe logs;
- an isolated-container old → new → old → new immutable-image sequence plus a
  clean standard-Linux/rootful-Docker CI host sequence through the actual
  installer, proving state preservation, capability unavailable on rollback,
  and forward recovery;
- full diff, security, privacy, secret, wire, existing-user, migration,
  backup/version/conflict/tombstone, and rollback review before merge.

Publication, registry digests, SBOM/vulnerability results, production
helper-first rollout, and production rollback evidence belong to the next
milestone and must not be inferred from this source-readiness document.
