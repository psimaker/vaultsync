# M4 Docker host-bind confinement proof

`run.sh` builds a local test-only image and runs three network-isolated phases:

1. an explicit installer phase receives one selected Syncthing folder and
   creates the exact `VaultSync Diagnostics` child after a test consent verdict;
2. a dormant runtime phase receives only that exact existing child, a separate
   state directory, and a read-only test config file; and
3. an adversarial phase overlays the fixed `installations` child with another
   bind mount and proves the changed Linux mount identity is rejected.

Every container uses a read-only root filesystem, the invoking uid/gid, no
network, no Linux capabilities, and `no-new-privileges`. The harness mounts no
Docker socket and publishes no port or image. It never starts the production
helper, Syncthing, Cloud Relay, APNs, StoreKit, Trigger, status, or probe flows.

Run from the repository root:

```sh
notify/tests/m4-docker/run.sh
```

This evidence applies only to an explicit host bind of an exact existing
subdirectory. Docker named volumes, rootless Docker, NAS packages, macOS and
Windows packaging remain unsupported for the diagnostics namespace.
