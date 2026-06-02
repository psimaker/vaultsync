# Cloud Relay activation — release checklist (1.5.1)

> **Status:** the activation feature (B-Weg startup-announce + A+C config.xml auto-detect + key-free
> setup) is **integrated locally on `feat/relay-activation`** (1.5.1 base). It is **not yet released**.
> This note records what was verified and the gates that must clear **before App Store submission**.
> Decision (owner, 2026-06-02): integrate now, document the risk; close these gates before shipping.

## ⚠️ Must clear before App Store release

1. **Receive leg untested on real hardware.** The path Relay → APNs → `didReceiveRemoteNotification`
   → `markReceived` → "Cloud Relay active" has only ever been exercised on the **send** side. The iOS
   Simulator does **not** deliver `content-available` silent pushes to the app handler, so the flip to
   "active" cannot be verified there. **Gate:** on a real device with a StoreKit **sandbox**
   subscription, run `vaultsync-notify` against the relay and confirm the startup-announce actually
   flips the app to "Cloud Relay active". Until then, the activation UX is verified end-to-end only up
   to the relay's `202`.
2. **`aps-environment` is `development`.** It is `development` in `ios/project.yml` / entitlements
   (pre-existing on `main`, deliberately not flipped). A blind flip to `production` can break silent
   push for paying users. **Gate:** per-config entitlements (Debug = development / Release =
   production) + **TestFlight** verification — not a blind flip.
3. **es / zh-Hans copy for the new key-free setup is a machine-quality draft.** The four new
   `RelayServerSetupView` strings (key-free command, uid hint, "It activates itself") need a native
   review for es and zh-Hans. de is reviewed; en is the source. (`plutil -lint` passes; key sets are
   lockstep across all four catalogs.)

## Platform / robustness follow-ups (not release blockers, but document for users)

4. **Compose `user:` default is `1000:1000`.** Correct for the official `syncthing/syncthing` image,
   but **wrong** for linuxserver/Unraid (PUID/PGID default **99:100**) and Synology (`sc-syncthing`).
   Those users must set `PUID`/`PGID`. **Synology/QNAP** configs are not in the binary's candidate
   list — they need `SYNCTHING_CONFIG` (and the spec's Synology path `…/syncthing/var/config.xml` was
   wrong; correct is `/volume1/@appdata/syncthing/config.xml`). See `EVAL-bweg-ac.md` §5.
5. **`bootstrap.sh`** now `<gui>`-scopes its address `awk` (no more `http://dynamic`) and its candidate
   list matches the binary. Longer term, prefer the binary's detection over the shell script to avoid
   two detection paths.

## Verified locally (2026-06-02, all against the **mock** relay — never production)

- `notify`: `gofmt`/`go vet`/`go build`/`go test` green, incl. new first-boot-wait tests; no skips.
- Bare-binary + full Docker path against the real `syncthing/syncthing` **v2.1.0** image:
  auto-detect (key from `config.xml`, URL per-field) → startup-announce → mock `202`; API key never
  logged; `-u 1000` reads / `-u 99` denied over `:ro`.
- `RELAY_URL` guardrail: missing → exit 1, no trigger (preserved through the first-boot-wait change).
- App **Debug + Release** `BUILD SUCCEEDED`; Release binary audit clean: `RELAY_BASE_URL_OVERRIDE`=0,
  `/api/v1/trigger`=0, `relay.vaultsync.eu` present, `provision`/`health` present.
- L10n: all four catalogs `plutil -lint` OK, identical key sets (lockstep).

## Notes

- `RELAY_URL` has **no production default in the binary** (a forgotten env never triggers a relay).
  The shipped `docker-compose.yml` *does* default it to the production relay — that is intentional for
  real subscribers (a plain `docker compose up` then self-activates by sending one wake-up). When
  testing locally, override `RELAY_URL` to a mock so you never hit production.
- The app has **no trigger sender** (only provision/health); every silent push is a genuine relay
  delivery, so "Cloud Relay active" cannot be faked.
