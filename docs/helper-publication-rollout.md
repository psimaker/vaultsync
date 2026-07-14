# Helper 2.0.1 publication and helper-first rollout

This document defines the owner-gated publication, compatibility, rollback,
monitoring, and recovery contract for `vaultsync-notify` 2.0.1. This patch
supersedes 2.0.0 because that release did not expose D022's pending transcript
fingerprint to the local operator; no 2.0.0 artifact or tag is changed. It does not by
itself claim that publication ran. The GitHub release, its
`RELEASE-MANIFEST.json`, the exact workflow run, and the registry digest are the
canonical post-publication evidence.

VaultSync 2.0 remains NO-GO after this helper release. The release makes the
reviewed helper capability available first; no released app calls it, and no
upload, download, or roundtrip product evidence follows from publication.

## Immutable release candidate

The reviewed source manifest is [`../notify/release.json`](../notify/release.json).
It fixes:

- version `2.0.1` and tag `notify-v2.0.1`;
- image repository `ghcr.io/psimaker/vaultsync-notify`, with the only new tag
  `2.0.1`;
- five expected binaries for Linux amd64/arm64, macOS amd64/arm64, and Windows
  amd64;
- the previous public rollback tag, commit, and multi-platform image digest;
- the exact ten release asset names, including checksums, SPDX SBOM, image
  digests, source/artifact manifest, and rollout evidence.

The tag is created only after the publication-readiness PR is merged and points
at that exact merge commit. The manual workflow accepts only that existing tag
as its workflow ref, the exact manifest version, the repository owner as both
actor and triggering actor, and the literal confirmation value. The tag commit
must be a valid GitHub-verified commit and an ancestor of the freshly fetched
`origin/main`.

The workflow never creates or moves a tag. It never publishes a `latest` image,
never uses `--clobber`, and never pushes a version tag that already names a
different artifact. A retry may only reuse an existing image whose source and
version labels, embedded version, registry digest, and repository attestations
match the same release commit. Existing draft assets are reused only after
their GitHub asset digest matches the locally rebuilt byte content; missing
draft assets may be added, but no existing asset is replaced. If the release
is already public, a retry may only rebuild and compare identical bytes, rerun
the controlled rollout, and perform read-only verification; a missing or
different public asset aborts.

Repository-wide GitHub immutable-release enforcement is intentionally not
changed by this milestone. Immutability is instead enforced by the reviewed
one-shot workflow and by consuming the published SHA-256 digests. Changing a
repository security setting requires separate authorization.

## Expected published artifacts

| Artifact | Required proof |
|---|---|
| OCI index for Linux amd64 and arm64 | Exact index and platform SHA-256 digests; source/version labels; embedded `vaultsync-notify 2.0.1`; BuildKit provenance/SBOM plus GitHub repository-bound provenance and SPDX SBOM attestations. |
| Five static binaries | Rebuilt twice byte-identically from the tag commit, listed in `SHA256SUMS`, scanned, and covered by GitHub provenance and SPDX SBOM attestations. |
| `SHA256SUMS` | Exact checksum for each binary; installers fail closed if this asset or a local SHA-256 implementation is unavailable. |
| `SBOM.spdx.json` | SPDX 2.3 inventory for the release binary set. |
| `IMAGE-DIGESTS` | Image repository, version tag, OCI index digest, and Linux amd64/arm64 manifest digests. |
| `RELEASE-MANIFEST.json` | Release version/tag, exact source commit, image digest, rollback baseline, binary sizes/digests, SBOM digest, and expected asset set. The exact workflow run is recorded separately in rollout evidence. |
| `ROLLOUT-EVIDENCE.txt` | Exact old/new references and the supported-host upgrade, rollback, and forward-recovery results, including the observed legacy 1.8.0 endpoint-log boundary and the 2.0.1 sensitive-log assertion. |

The image runtime is a static scratch image built from a digest-pinned Go
builder. Its CA bundle is copied from that same pinned builder. There is no
runtime shell, package manager, or publication-time `apk` repository. The
publication build still scans the pushed multi-platform digest before the
release can leave draft state.

## Helper-first rollout

Publication is staged in this order:

1. Re-run source, policy, notify, vulnerability, secret, and image validation
   on the exact tag commit.
2. Build or safely resume the version-only multi-platform image; verify its
   exact digest, scan it, and establish repository-bound provenance and SBOM
   attestations.
3. Build every binary twice and require byte equality, scan the results, create
   checksums and SPDX SBOM, attest the binaries, and stage a draft GitHub
   release without replacing any asset.
4. On a fresh GitHub-hosted standard Linux runner using rootful Docker, execute
   the real explicit diagnostics installer against only digest references:
   published 1.8.0 → candidate 2.0.1 → published 1.8.0 → the same candidate
   2.0.1.
5. Require the old helper to keep diagnostics unavailable, the new helper to
   expose the TLS listener, the rollback to preserve credential-state bytes,
   and forward recovery to preserve both those bytes and the TLS SPKI pin. The
   candidate phases must not log any configured test credential, identifier,
   path, or URL. The proof separately records that the immutable 1.8.0 baseline
   retains its legacy configured-endpoint startup fields.
6. Add the immutable rollout evidence to the still-draft release, recheck the
   tag and every asset, publish the release, then perform a separate read-only
   download, digest, attestation, architecture, version, and availability
   verification.

The rollout uses only ephemeral test configuration, a deterministic mock local
Syncthing/Relay endpoint, and newly generated helper credential state. It does
not use a real vault, note, customer device, customer account, Syncthing share,
or production Relay request. It creates no namespace because no app pairing or
signed enablement request exists.

## Compatibility matrix

Diagnostics is additive and opt-in; Trigger v1 and Relay v1 remain unchanged.

| App | Helper | Relay | Result |
|---|---|---|---|
| Released old app | Published old helper 1.8.0 | Existing old Relay | Existing Trigger-v1 behavior only. |
| Released old app | Published helper 2.0.1, diagnostics unset | Existing old Relay | Byte-compatible Trigger-v1 behavior. No listener, credential, namespace, trust, mapping, or diagnostics artifact is created. |
| Released old app | Helper 2.0.1 explicitly configured | Existing old Relay | Helper capability exists locally, but the old app never calls it. Upload, download, and roundtrip remain unset. |
| Future capable app | Published old helper 1.8.0 | Any supported Relay v1 | Honest capability unavailable. The app must not pair, create, trust, or transfer. |
| Future capable app | Helper 2.0.1, unconfigured/unpaired/unauthorized | Any supported Relay v1 | Honest unavailable/unsupported response; no fallback evidence and no automatic action. |
| Future capable app | Helper 2.0.1, exact explicit pairing and namespace authorization | Existing old Relay | The local D022–D024 helper contract may be used only after the explicit local transcript comparison. Relay remains outside upload/download/roundtrip correlation. |
| Future capable app | Helper 2.0.1 | Future new Relay | Same local diagnostics contract; Relay version does not strengthen sync evidence. |
| Any app | Helper 2.0.1 → 1.8.0 | Any Relay v1 | Diagnostics becomes unavailable. Credentials, namespace content, mappings, backups, versions, conflicts, and tombstones are not deleted or rewritten. The rollback also restores 1.8.0's legacy configured-endpoint startup log fields; operators must apply their existing log-access controls. |
| Any app | Helper 2.0.1 → 1.8.0 → exact 2.0.1 digest | Any Relay v1 | Forward recovery revalidates preserved state and requires current exact credentials/authorization. No operation resumes automatically. |

The five downloadable binaries do not expand diagnostics packaging support.
Docker Host-Bind on a standard Linux host with rootful Docker remains the only
supported diagnostics row. Rootless Docker, Docker Desktop, WSL, NAS/FUSE or
remote storage, named volumes, systemd binaries, macOS launchd, and Windows
Scheduled Tasks remain diagnostics-unsupported.

## Monitoring and abort criteria

Publication aborts before the GitHub release becomes public on any of:

- tag, source commit, owner, manifest version, or main-ancestry mismatch;
- an existing version image or release asset with a different digest;
- a real secret finding, HIGH/CRITICAL fixed vulnerability, failed source or
  packaging test, missing provenance/SBOM, or invalid repository attestation;
- missing/extra release asset, binary non-reproducibility, checksum mismatch,
  or image architecture/version/label mismatch;
- failure of upgrade, rollback, forward recovery, state-byte preservation,
  TLS-pin preservation, mount constraints, 2.0.1 sensitive-log exclusion, or
  exact installer use. The separately asserted legacy endpoint fields from the
  immutable 1.8.0 rollback image are a documented rollback boundary, not
  candidate evidence.

After publication, the read-only verifier must observe the public release,
exact tag commit, all ten assets and GitHub-reported SHA-256 digests, both image
architectures, the recorded OCI digest, all attestations, and the embedded
version. A failure keeps every app runtime milestone blocked. Already published
content is not overwritten to hide a partial failure; recovery uses the same
commit and verifies existing bytes before adding only missing draft material.

Because this helper is user-run rather than a centrally deployed service,
monitoring covers artifact availability, integrity, attestations, workflow
health, and the controlled supported-host rollout. It cannot claim customer
installation success, Relay delivery, APNs delivery, iOS background execution,
or user-vault progress.

## Existing-user, migration, and retention impact

Existing installs are not migrated, paired, restarted, or reconfigured by
publication. The historical `latest` image tag is not moved. A user or operator
must explicitly rerun the installer, select the reviewed `2.0.1` version tag,
or pull the documented digest. The installer re-resolves the version tag and
runs the resulting local content ID; a network failure does not silently reuse
a stale tag.

Without both diagnostics configuration paths, 2.0.1 behaves as the prior
Trigger-v1 helper and creates no diagnostics state. With explicit configuration,
pairing, namespace enablement, and later app operations remain separate signed
actions. No helper publication discovers Syncthing, changes its configuration,
shares a folder, transfers trust, or creates/adopts a namespace.

Rollback does not remove helper credentials or synchronized content. Backups,
Syncthing versions, conflicts, peers, remote history, and tombstones can retain
opaque diagnostics artifacts. Cleanup remains digest-directed and
evidence-orthogonal; neither a cleanup acknowledgement nor a signature proves
transport route, exact synchronized bytes, direct peer identity, future
delivery, or global health.

Emergency rollback also returns to 1.8.0's existing startup logging of
configured endpoint values. It does not log the API key in this proof, but
endpoint values can still be private deployment metadata. Restrict old-helper
log access and forward-recover to the exact 2.0.1 digest when the abort cause is
cleared; 2.0.1's rollout gate rejects those configured values in candidate
logs.

## Evidence boundary after helper rollout

| Claim | State |
|---|---|
| Helper publication | Proven only after the exact public release, image, binaries, digests, attestations, and post-publication verifier succeed. |
| Helper upgrade/rollback/recovery | Proven for the single supported standard-Linux/rootful-Docker test row with ephemeral controlled data. |
| Authenticated capability | Helper-side runtime proof only; no released app has paired. |
| Upload | Unset in the product. |
| Download | Unset in the product. |
| Roundtrip | Unset in the product. |
| Cleanup | Helper-side exact-digest mechanism only; never directional sync evidence. |

The next app milestone remains blocked until the public helper release and the
complete helper-first rollback/forward-recovery workflow are both successful.
