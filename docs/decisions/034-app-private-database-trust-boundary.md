# 034 — App-private database ownership in 2.0.2

- Context: The 2.0.2 receive-side hard floor must define which writers can reach the embedded engine database (#150/#167).
- Decision: Supported iOS operation keeps configuration and database files in VaultSync's private app container, outside security-scoped vaults, and opens them through one main-app engine owner; the widget has no engine or database access.
- Validation: Recognizable schema, folder identity, alias, or integrity deviations stop with the stable path-free safety code before mutation.
- Configuration: Existing protected folders permit only one-shot folder-, operation-, and where applicable device-bound Remove, Pause, Share, or Unshare diffs; every extra diff and all path, filesystem, type, ignore, or rescan changes remain denied.
- Boundary: Vault files and external vault editors remain inside the receive-side protection model; the internal database assumption applies only to the app-exclusive production path above.
- Why: The product has no helper, extension, second engine process, file-sharing route, or supported external workflow that writes the internal engine directory.
- Rejected alternative: Add database authentication, quarantine, snapshots, persistence migration, or transactional recovery to the containment release; generation-bound versions of those designs belong to Vision 3.0.
- Links: issues [#150](https://github.com/psimaker/vaultsync/issues/150) and [#167](https://github.com/psimaker/vaultsync/issues/167); decisions 032 and 033.
