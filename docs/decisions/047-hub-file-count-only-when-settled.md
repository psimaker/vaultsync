# 047 — The Hub reports a file count only for a settled vault; unknown is -1

**Context.** Devices decide from the Hub's file count whether a folder that already holds files may join a vault (`join.AcceptShare`: 0 = the local content becomes the first copy, > 0 = refused, < 0 = refused as unknown); the desktop agent's new-vault evidence (046) requires 0 right after the create and again before the add. Syncthing's local count is 0 for an empty folder *and* for one whose first scan has not finished, it omits files other devices announced but the Hub has not downloaded, and the catalogue left 0 when the status could not be read (#214).

**Decision.** `vaultFiles` reports the global count — what the vault holds or still expects — only for a settled folder: idle, with a completed scan (`/rest/stats/folder` lastScan); otherwise, and whenever the status cannot be read, -1. One exception: a vault this Hub process created as a fresh, empty directory reports the global count until its first scan completes — nothing local can be in it — so the agent's immediate check stays deterministic; afterwards the rule applies to it too (a rescan reads as unknown). The provision reply waits up to 2 s for the folder that sharing just restarted. CLIs print "counting…" for -1.

**Why.** 0 is the one value that lets a device merge; it must never stand for "not known yet". Global instead of local: files another device announced make a vault non-empty whether or not the Hub has them.

**Rejected alternatives.** Reporting 0 for a created vault without looking (ignores an index that already arrived). A "scanning" flag on the wire (a protocol change old agents and the iPhone would not understand; -1 already means unknown to every consumer). Waiting for the first scan inside create (blocks the reply; the stats-based rule needs no wait).

**Links.** #214, #223, #206, #175; decisions 007, 046.
