# Changelog — VaultSync Hub

The Hub's coordinator `vaultsync-hub`, released as `hub-vX.Y.Z` tags: the image `ghcr.io/psimaker/vaultsync-hub:X.Y.Z` and the `vaultsync-hub_<os>_<arch>` programs on the GitHub release. How to move a Hub to a release, and what carries over: [docs/hub.md → Update the Hub](../docs/hub.md#update-the-hub).

What the setup link's **2) Hub** and the Hub's compose stack (`hub/scripts/setup.sh`, `hub/docker-compose.yml`) change is in the [root changelog](../CHANGELOG.md): the setup link serves the main branch, so such a change reaches Hub owners the moment it is merged, with no Hub release in between. A change that reaches people both ways has a line in each change log: a Hub release that the setup link then installs (what the release brings here, the new default there), or a fix in the pairing packages that VaultSync on iPhone and VaultSync for Mac and Linux are built with too.

---

## [Unreleased]

## [0.3.1] — 2026-11-16

### Changed

- **Built with Go 1.26.11** — Go 1.26.6 → 1.26.11 with the latest standard-library security fixes; the [root changelog](../CHANGELOG.md) lists them under 3.0.0.

### Fixed

- **A device's name can no longer break a line or move the address shown beside it** ([#292](https://github.com/psimaker/vaultsync/issues/292)) — a name comes from whatever answers on the network, and it kept characters that are not part of a name: a line separator, a mark that reverses the text after it, control characters. The Hub now drops them from the names it keeps for the devices that pair with it; names it stored before stay as they are. What a name is written with stays: letters of any script, emoji, and the joiners some of them need. `vaultsync-hub pair` shows each Hub that answered with its address first: a name can hold anything that looks like an address, and one in Hebrew or Arabic letters drew the real address behind itself in a terminal that follows a text's direction. The command also no longer prints a Hub's name, or hands it to the local Syncthing, as it came. Pairing itself is unchanged: Hubs and devices with and without this fix pair with each other as before. Recorded in decision 061.
- **A Hub's error message is printed as one short line** ([#319](https://github.com/psimaker/vaultsync/issues/319)) — when pairing fails, `vaultsync-hub pair` prints the message of the Hub's answer. That message comes from whatever answers at the address that was dialled, and it was printed as it came: with control characters, line breaks or a mark that reverses the text after it, and as long as an answer may be. It is now one line without those characters, cut to 300 bytes. The same holds for the reason a Hub gives when it could not share a vault or register a device, where it passes on what its Syncthing said. The messages a Hub words itself read as before, a name in them with its spaces as written; one of more than 300 bytes ends in "…". Recorded in decision 062.

## [0.3.0] — 2026-10-07

### Fixed

- **The Hub no longer reports a vault as empty unless it has proof** ([#214](https://github.com/psimaker/vaultsync/issues/214)) — a device that pairs a folder with files into an existing Hub vault relies on the Hub's file count, and that count was 0 while an adopted vault's first scan was still running, while the Hub's database could not be read, and for files other devices had announced but the Hub had not downloaded yet. The Hub now counts what it holds or expects, and reports 0 only when its index knows of no file, folder or link anywhere, the vault's directory on the Hub is empty at that moment (and not reached through a link) and the folder is running; otherwise it says it does not know, which VaultSync for Mac and Linux and `vaultsync-hub pair` treat as "not empty" — the merge is refused with a retry hint — and the iPhone shows as no count. Starting a new vault from a computer keeps working: the vault the Hub just created is empty on disk from the first moment. `vaultsync-hub status` and `vault list` show "unknown" for a vault the Hub cannot prove empty (a vault it just created empty counts 0), and "1 file" instead of "1 files" ([#223](https://github.com/psimaker/vaultsync/issues/223)). Recorded in decision 047.
- **`vaultsync-hub pair` no longer spends your code on every Hub that answers** ([#204](https://github.com/psimaker/vaultsync/issues/204)) — the command tried the code on each Hub on the network in turn, so a second Hub counted a wrong code (five lock it) for a code that was never meant for it. It now asks which Hub printed the code when several answer, and without a terminal it stops and asks for `--hub`, like VaultSync on iPhone.

## [0.2.0] — 2026-10-06

### Added

- **The Hub prints a QR code and a pairing link next to every pairing code** ([#174](https://github.com/psimaker/vaultsync/issues/174), [#205](https://github.com/psimaker/vaultsync/issues/205)) — `vaultsync-hub code` now prints both, for Add Hub in VaultSync on iPhone: type the code or scan the QR code. When the Hub knows its local address it puts it into the QR code, so the iPhone skips the search for the Hub; `--address` names the address when the default one is wrong, and `--no-qr` leaves the QR code out. Recorded in decision 045.

## [0.1.0] — 2026-09-14

### Added

- **VaultSync Hub: a coordinator that pairs your devices with a short code** — `vaultsync-hub` runs next to the Hub's own Syncthing in the stack the setup link installs. It applies the Hub defaults to that Syncthing (unlimited conflict copies, staggered versions kept for 30 days, no usage reporting), answers devices on the local network and prints a pairing code such as `TULIP-ANCHOR-42`. A device turns the code into a session key with SPAKE2 (RFC 9382) and lets the Hub create or share a vault; `vaultsync-hub pair` does that for a computer that runs its own Syncthing, and accepts the vault into an empty directory. Guards match the app's doctrine: vaults live only under one root, never overlap, are never created over existing content without an explicit `vault adopt`, and the Hub never changes a folder's path or deletes anything. Codes expire after 24 hours and lock after five wrong attempts; the pairing port answers only private, loopback and link-local source addresses and must not be forwarded. `status` and `vault list`, `create` and `adopt` complete it. Documented in `docs/hub.md`; decisions 035–037.
