# 073 — The viewer saves a note only over the version it opened, and keeps the person's text until the engine has the save

**Context.** #347 gives the desktop apps a window that shows the notes VaultSync syncs, and turns a note into its Markdown text to edit with one switch. Edit mode writes into notes that the sync engine writes at the same time. A newer version can arrive from another device, and another app on the computer can write the note. A save that replaces a version the person never saw loses that version on every device. The engine scans the saved note as this computer's change, a successor of the version it replaced, and sends it out.

The engine is stock Syncthing, pinned in `desktop/pins.go` (v2.1.7). Before it puts a received version in a note's place, it compares the note on disk with its index by size, modification time and permissions, not by content (`performFinish` → `scanIfItemChanged`, `IgnoreBlocks: true`). When they differ it rescans instead of replacing. Between that check and its own rename nothing is locked. A received version keeps the sender's modification time, set with `Chtimes`.

A name that begins `.syncthing.` or `~syncthing~` is never scanned and never sent. The scanner deletes such a file once its modification time is 24 hours old (`lib/fs/tempname.go`, `lib/scanner/walk.go`, `keepTemporariesH`). When the engine deletes a directory for a change from another device, it deletes such files in it at any age (`deleteDirOnDiskHandleChildren`). A conflict copy (`.sync-conflict-…`) is deleted by the engine when it conflicts again, and is pruned beyond `maxConflicts` (`moveForConflict`). On a computer, VaultSync's folders keep every conflict copy (`maxConflicts` −1, `hub/join`); the iPhone keeps 10 (decision 035), and its "always skip" removes every conflict copy of a file. A computer's folder scans a change 10 seconds after the file watcher sees it (`hub/join`). The Hub keeps replaced versions for 30 days (decision 035).

**Decision.**

1. **Who writes.** The save runs in the service, which also owns the engine (decisions 046, 049). It never runs in the window. Two reasons: the recovery in (4) must run before the engine starts, and the check in (6) reads the engine's index. The save writes the note open in edit mode and nothing else in the vault, apart from its own temporary files of (4). The engine neither scans nor sends those, and they do not outlive a save. The one other write is a new note that holds the person's text, made only when the person chooses it in (7).
2. **The baseline.** When edit mode is entered, the note's bytes are read once and their SHA-256 is kept as the baseline. The baseline is the version the person starts editing, not the one first shown. A save that has passed its check in (4) makes the saved text the new baseline, and edit mode stays open after Cmd-S or Ctrl-S. A version on disk equals the baseline when its bytes do. Modification time and size never decide this, because a newer version can arrive with an older time and the same size.
3. **When it saves.** A note is saved when the person leaves edit mode, presses Cmd-S or Ctrl-S, or closes the note or the window. It is never saved while the person types. While they type, the text goes every few seconds to a draft in VaultSync's own folder, outside the vault (mode 0600, as `agent.json`), so a crash or a power cut loses nothing. Nothing in the vault changes before a save. Leaving edit mode without a change writes nothing.
4. **The write.**
   - **The new text.** It goes into a new file in the note's directory, named with the engine's temporary prefix and a random part that is no note's temporary name (`.syncthing.` plus the random part plus `.tmp`). The engine neither scans nor sends such a file. The file takes the note's permission bits and extended attributes (Finder tags) and is flushed to disk.
   - **The record.** The service writes the exchange down in its own folder: the note, the temporary name, the baseline and the hash of the new text.
   - **The exchange.** One atomic exchange swaps the new file with the note: `renamex_np(RENAME_SWAP)` on macOS, `renameat2(RENAME_EXCHANGE)` on Linux. On Windows the call is settled with the package record of #217, and it must keep the same guarantee.
   - **The check.** After the exchange, the temporary name holds exactly the version the save replaced, and it is hashed:
     - **Equal to the baseline:** the save took the place of the version the person opened. The replaced version is deleted.
     - **Different:** a newer version arrived between the open and the save. The exchange is undone at once, which puts the newer version back in the note's place. The person's text stays in the window and in the draft.
   - **After a crash.** A service that starts and finds such a record resolves it before it starts the engine. On Linux the kernel stops the engine when the service dies (`Pdeathsig`, `desktop/child_linux.go`); on a Mac launchd stops the service's process group with it (`desktop/child_other.go`). The person's text is in the draft in every case. Recovery hashes the temporary file before it deletes it, and deletes it only when it equals the new text (the draft holds it) or the baseline (the version the person's save replaced).
     - **The note holds the new text:** the exchange happened.
       - A temporary file equal to the baseline is deleted, and the save goes on to (6).
       - A temporary file that differs is a displaced newer version. The exchange is undone, and the temporary file, which then holds the new text, is deleted. The person gets the choice of (7) when the note opens next.
     - **The note holds something else:**
       - A temporary file equal to the new text means the exchange never happened, or was undone. It is deleted.
       - A temporary file equal to the baseline means the exchange happened and the note was written again afterwards. The save counts as overwritten, as in (6); the temporary file is deleted, and the person gets the choice of (7).
       - A temporary file equal to neither is copied into VaultSync's own folder, outside the vault, and only then deleted. The person is told where it is.
     - **The temporary file is gone while the note holds the new text:** what the save replaced cannot be checked, and the person is told so.
     - Recovery never publishes anything as a new note, and the engine never runs while a displaced version lies under a name it deletes.
   - **The modification time** is the time of the save. The old time is never restored, because the engine notices a change by it.
5. **No exchange, no edit.** Some file systems offer no atomic exchange; that is expected of exFAT and of network and FUSE file systems, and is to be measured. Edit mode tests the exchange when it is entered, with two empty temporary files of its own in the note's directory, so a refusal comes before the person types. A save's own exchange fails the same way, before anything changes. The save then says that this folder cannot save a note safely, the text stays, and edit mode stays off for that vault. Reading works as before. There is no fallback that checks first and renames after.
6. **Done means the engine has it.** A save is done when the engine's own entry for the note (`GET /rest/db/file`, `local`) was made by this computer, with the size and modification time the save left. While the folder does not sync (paused, engine stopped) a save is done once the bytes on disk read back equal to the saved text: on its next pull the engine checks the note before it replaces it and keeps both versions as a conflict. Until a save is done, the draft stays.
   - **When the engine replaced the save.** The engine may instead record, after the save, a version received from another device without this one before it. Its pull then checked the note before the save and replaced it after. The save counts as overwritten, and the person gets the choice of (7) with their text from the draft.
7. **What the person sees.**
   - **Reading mode.** A note that changed on disk is shown again. Reading mode writes nothing.
   - **Edit mode.** As soon as the note on disk no longer equals the baseline, a bar says that the note changed since it was opened and that the person's text is kept. The service watches the note and the engine's events, and the save in (4) checks again in any case. The person has two choices:
     - **Open the newer version.** Their text stays in the draft and can be brought back into the editor over it. The newer version is then the version they opened, the baseline of (2), and a save replaces it as any save does.
     - **Keep both.** Their text becomes a new note beside the old one, named `<name> (edited YYYY-MM-DD HHMM).md` and written without replacing anything. The engine does not treat that name as a conflict copy, so no prune and no conflict rule deletes it.
   - No choice replaces the newer version unseen, because #347 promises that a note that changed since it was opened is never overwritten. Nothing is merged.
8. **Deleted or moved underneath.** A save never writes a note back to a path where it no longer is. The save stops, the text stays, and the person may keep it as a new note as in (7). A save does not undo a deletion or a move made on another device.
9. **Read-only notes.** Some notes open in reading mode only, and the window says why:
   - a note that is not valid UTF-8 or holds a NUL byte;
   - a symbolic link;
   - a file with more than one hard link (the exchange would split it);
   - a note larger than 1 MiB (the bound of decision 041).
10. **Bytes outside the edit stay.** Text the person did not touch is written back as it was read: its line endings, a byte order mark, whether the note ends with a newline, and its Unicode normalization. A note the editor cannot hold byte for byte opens read-only.

**Why.**
- **Decisions 002 and 007.** A version the person did not see is never replaced without their decision, and nothing is merged.
- **The exchange.** It turns the last check into a fact about the very file it replaced. A check before a rename only describes a file that may already be gone.
- **The draft.** The engine has a window of its own between its check and its rename, which no writer outside the engine can close. The draft turns that window from a loss into a question the person answers.
- **Saving at the end of an edit.** Every save is a version that every device receives. Saving at the end of an edit keeps half a sentence off the phone and opens the windows of (4) and (6) once per edit, not once per pause in typing.
- **The temporary name.** It keeps the new text and a displaced version away from every other device. Any other name inside the vault is scanned and sent within seconds.
- **Recovery before the engine.** The engine deletes a temporary file by its age, and a displaced version keeps its own, possibly old, time.

**Rejected alternatives.**
- *Check, then rename over the note:* the engine's rename can land between the check and the rename, and the version it brought is lost on every device without a sign. Decision 027 rejected the same for Keep Both. It is also rejected as a fallback where the exchange is missing (5).
- *Writing into the note in place:* a reader, whether the engine's scanner or another app, can see half a note, and a crash leaves half a note that syncs.
- *Saving while the person types, as Obsidian does:* every pause would become a version on every device.
- *Keeping both under the engine's conflict name:* the iPhone's conflict list would show it, but the engine and the iPhone app delete conflict copies by their own rules. The only copy of the person's text must not lie under such a name.
- *A choice that replaces the newer version with the person's text:* #347 rules it out. Bringing the text back over the opened newer version does the same with that version seen.
- *Merging the two versions line by line:* decision 002.
- *Pausing the folder for each save:* it stops all of the folder's sync, and a folder's pause is the person's own setting (decision 049).
- *A temporary file in VaultSync's own folder:* it can lie on another volume, where a rename is a copy and not atomic.
- *The save in the window:* its program would need its own access to the vaults, and the recovery in (4) could not come before the engine starts.

**Not covered.**
- **The engine's own window.** A save that lands between the engine's check and its rename is replaced by the engine. The save cannot prevent that; (6) notices it and the person's text is kept.
- **A displaced newer version.** It lies under the temporary name between the exchange and its undo. A scan that passes in that instant deletes it if its modification time is 24 hours old. The engine also deletes it at any age when it deletes the note's directory for a change from another device in that instant. The undo then fails, and the person's text stands in the note's place as a successor. The person is told. The other devices receive that text and replace the newer version too. Only a Hub in the folder's set keeps it, for 30 days (decision 035), or a device whose folder has versioning of its own.
- **Another app that holds the note open** and writes into the file it opened after the exchange writes into the replaced version. The check in (4) catches that only if the write comes before the hash.
- **A service started by hand on a Mac,** outside launchd. If it dies, its engine may outlive it, and the recovery in (4) can then run while that engine still runs.
- **Windows**, until #217's package record names its exchange.

**Read, not measured.** Nothing here was run; there is no prototype.
- **The engine's behaviour** was read in stock Syncthing v2.1.7's source (tag `v2.1.7`, commit `d6d0181f`): `performFinish`, `scanIfItemChanged`, `moveForConflict`, `deleteDirOnDiskHandleChildren`, `lib/fs/tempname.go`, the scanner's temporary rule, and `getDBFile` with the fields `modifiedBy`, `size`, `modified`. A pin bump reads them again.
- **The exchange** was read in the `rename(2)` and `renamex_np(2)` manual pages. Still to measure:
  - which file systems offer it (APFS, ext4, btrfs and XFS are expected to; exFAT, SMB, NFS and FUSE are not);
  - that it keeps each file's own mode and extended attributes.
- **Obsidian.** Whether Obsidian, open on the same note, follows the note's new file is not known.
- **The engine stopping with the service.** That it stops with the service on a Mac is taken from the code's comment on launchd's process group, not measured.

To measure before the first line of code:
- the exchange on each of those file systems;
- the engine's entry after a save, and after a received version replaced a save;
- Obsidian open on a note while it is saved;
- the draft's frequency.

**Links.** #347; decisions 002, 007, 027, 035, 041, 046, 049; the records "The viewer's window …" and "What the viewer shows and writes" (#347).
