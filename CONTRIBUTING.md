# Contributing to VaultSync

Thank you for considering a contribution — it genuinely helps. VaultSync is maintained by one person, and community input is one of the best ways this project gets better.

One thing shapes everything here: VaultSync syncs people's personal notes across their devices. A bug in the wrong place doesn't crash a demo — it can damage someone's vault. That's why a few areas move deliberately slowly (explained below), and why your patience with the process is appreciated.

## Contributions that help the most

- **Bug reports** — especially with the helper's self-check attached (`vaultsync-notify --doctor`), your iOS and VaultSync versions, and your server's Syncthing version.
- **Documentation improvements** — setup and troubleshooting docs get better with every real-world deployment they describe.
- **Deployment experience** — running the helper on a NAS (Synology, QNAP), via Docker Compose, or on unusual setups? Notes about what worked and what didn't are valuable.
- **Testing on different platforms** — the helper ships for Linux, macOS, and Windows; coverage across them is hard for one person.
- **Translation review** — the app is localized in English, German, Spanish, and Simplified Chinese. Native-speaker review of existing strings is very welcome.

## Please open an issue first

For anything beyond a trivial fix (a typo, a broken link, an obvious one-liner), **please open an issue before writing code**. Two honest reasons:

1. Review time is limited. An issue first makes sure your work fits the project's direction *before* you invest in it — nobody enjoys a rejected PR that took a weekend.
2. Central areas are governed by binding decision records in [`docs/decisions/`](docs/decisions/): the wire contracts, the diagnostics protocol, and the data-safety invariants are frozen. Changing them requires a new decision record and maintainer approval. That's not "hands off" — it means: open an issue and we design the change together.

Reading the decision records before proposing a change in those areas will save you time; each one explains *why* the constraint exists.

## Development setup

An honest picture of what you can build where:

**Go components — Linux or macOS, Go 1.26+:**

```bash
# Server helper (its own Go module)
(cd notify && go test ./... && go vet ./...)

# Sync bridge (applies the Syncthing patches first; ~1 min, starts real Syncthing instances)
(cd go && make patch && go test -tags noassets ./bridge)
```

`gofmt` is enforced by CI for both modules.

**iOS app — macOS only**, with Xcode 26+, XcodeGen, gomobile, and Make:

```bash
(cd go && make patch && make xcframework)   # builds the embedded sync engine (~160 MB)
(cd ios && xcodegen generate)
(cd ios && xcodebuild test -project VaultSync.xcodeproj -scheme VaultSync \
  -destination 'platform=iOS Simulator,name=iPhone 17 Pro,OS=latest' CODE_SIGNING_ALLOWED=NO)
```

The full build, signing, and test guide is [`docs/setup.md`](docs/setup.md). If you only touch the Go side, you don't need a Mac at all.

## Pull request conventions

- **PR titles must be Conventional Commits** — CI enforces this, and since PRs are squash-merged, your title becomes the commit subject on `main`. Allowed types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `perf`, `build`, `style`, `revert`. Example: `fix(notify): handle missing config path`.
- **Small, focused PRs** — one concern per PR reviews faster and merges sooner.
- **Behavior changes need tests.** Bug fixes should include a regression test that references the issue number.
- User-facing changes get a `CHANGELOG.md` entry under `[Unreleased]`.

## Localization

Every user-facing string ships in four languages: `en`, `de`, `es`, `zh-Hans`.

- The **English literal string is the key**. A key missing from any of the four `Localizable.strings` files silently falls back to English.
- All four files must contain **identical key sets** — CI checks this (Strings Key Parity), and you can run it locally: `ios/scripts/strings-key-parity.sh`.
- German uses the informal du-form, matching the existing strings.

## Security issues

Please **do not** open a public issue for anything security-relevant — use the private channels described in [SECURITY.md](SECURITY.md) instead.

## Code of conduct & license

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). Contributions are licensed under [MPL-2.0](LICENSE), the project's license.
