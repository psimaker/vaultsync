# 088 — A removed folder is offered again from the device's last announcement, without a reconnect

**Context.** After "Remove Vault", a device that still shared the vault did not offer it again until the next connection to it, in practice until VaultSync was closed and opened again (#316, seen on a device; #341). The engine writes a pending entry only when a device's cluster configuration arrives (`ccHandleFolders`), and a device sends one when a connection is set up or when it starts or stops sharing a folder with us or pauses one: a change of a folder's devices or its pause restarts the folder, and a restart sends it. A rename does not (`Label` is `restart:"false"`). A removal sends *our* cluster configuration and then discards every device's pending entry for the folder (`cleanPending`). The device has no reason to answer. The removal also stops the folder's index handler on every connection, and with it goes the start info that handler was started from (`indexHandlerRegistry.Remove`). Since #340, every text that sends people to remove a vault and accept it again tells them to wait ten seconds and close VaultSync between the two steps. The first attempt (#339, closed) closed the connections to the folder's devices so that they would announce it again. Its review found that the costs all come from the reconnect:
- It spends the engine's forced redials: three per device in two minutes, then none for five. After that, a device reached only through a relay is dialed again after ten minutes.
- The close message is lost behind an unacknowledged write. A peer that also misses the reset refuses the redial until its own ping notices.
- Two dials at once meet as one connection, or as two of which one is closed (#185). One run in forty stood still for a minute.
- The other vaults stop for each removal.
- The devices it reconnects are not the ones that announced the folder.

**Decision.**
1. **Fork patch `009-a-removed-folder-is-offered-again-from-the-last-announcement`.** The engine offers a removed folder again itself. It uses what each connected device announced for the folder in its last cluster configuration on the connection that is up: the label, the encryption flags, whether the folder runs there, and the two devices' index information.
   - The connection's index-handler registry keeps that announcement. It is noted when a cluster configuration is first handled, every one replaces it whole, and it ends with the connection.
   - **One lock per connection holds the replay and a newer announcement apart.** `ccHandleFolders` reads the device's pending entries (`PendingFoldersForDevice`), handles the announced folders, and at its end expires the entries the announcement no longer holds. The replay reads the announcement and writes the pending entry. Without a lock, a cluster config that drops the folder could list the pending entries before the replay wrote its own, and leave that entry standing: an offer the device no longer makes, whose accept the device refuses at its first index. So the handler takes the registry's announcement lock before it lists and holds it to its end, and the replay holds the same lock from its read to its write. Either the replay comes first, and the handler expires what it wrote, or it comes second, and it reads the newer announcement. The rejected way: store the announcement first and re-check it after the write. A re-check closes the gap only if the announcement is stored before the listing, and it leaves an instant in which the stale entry is visible.
   - After `cleanPending`, `CommitConfiguration` writes two things for each removed folder and each such device, exactly as the arrival of that cluster configuration does for a folder this side does not have: the pending entry, and the index start info (only where the folder runs on that device). Then it sends one `PendingFoldersChanged` event.
   - It skips a device that ignores the folder, and a device removed or paused in the same change: a paused device has no connection, so upstream writes nothing from it either.
   - It closes no connection and writes no configuration.
2. **The removal forgets the folder's state in every connection's registry,** not only its index handler and its start info (`Remove` keeps `folderStates`). The start info then waits as a pending folder's start info does, and nothing starts for a folder that is not configured. Without it, the replay's start info starts an index handler at once for the removed folder, which makes the folder's database again (`GetIndexID` opens it with create set). `TestIssue341_TheRemovedFoldersDatabaseIsNotMadeAgain` fails then.
3. **The app accepts nothing it did not accept before.** A removed folder's ID stays in `userRemovedFolderIDs` until an explicit accept (006). The offer, now back when the removal returns, is listed and waits for a tap.
   - `AcceptPendingFolder` shares the vault with every device whose offer is pending.
   - The accept runs on the connections that are up. The kept start info lets the index exchange begin there. Without it, the device's first index for the folder closes the connection with `ErrFolderMissing`.
4. **It relies on #343.** The app records a removal before the engine is asked and keeps the record whatever the answer (006), so the automatic pass never takes the offer that comes back.
   - The replay runs inside the commit that removes the folder, at the end of the model's part of it: after the folder has stopped and its database is gone. No offer exists before the removal has reached the configuration.
   - For a removal answered "not confirmed", it runs when that commit lands, however late, and not at all if the commit never lands.
   - An accept tapped on the returning offer can still find the rest of that commit in the engine's queue. Since #462, decision 006 says what such an accept answers when it outlasts the bridge's wait, and that it is never sent twice.
   - Until the tap, nothing of the folder is exchanged with any device. Our cluster configuration no longer names the folder, so each device stops its index handler for it, and this side keeps only start info, which starts nothing before an accept.
5. **The texts of #340 go back to accepting the share right away,** in a change of their own. The vault is accepted under Pending Shares once it shows there, which it does while a device that shares it is connected.

**Why.**
- The offer is the device's own. A device sends a new cluster configuration whenever it starts or stops sharing a folder with us, or pauses or resumes one. Its last one on a connection that is up is therefore its current announcement of every folder: the same one a reconnect would bring, except for a label changed since. A reconnect would pay all its costs for information the engine already holds.
- Nothing is closed, so the transport does not matter. A device on a relay is offered again the same way as one on the local network.
- Every connected device that announced the folder is offered again in the same commit, so one accept shares the vault with all of them.
- What the tests show (`TestIssue341_…` in `go/bridge`), against the peer built from the pinned module and against stock Syncthing 2.1.7 and 1.30.0 — a Hub on either line takes the replayed offer's accept on the connection that is up:
  - The offer is there when `RemoveFolder` returns, also for five vaults removed one after another.
  - The other vault syncs both ways after the removal, and the vault accepted again syncs on the same connection: the peer reports the connection it set up before the removal.
  - Until the tap, neither side logs an index received for the folder, download progress or a pull of it, while the peer changes it. After the tap, both do.
  - Whatever the peer does — stops sharing or pauses the folder, before the removal or once the offer is back — what is pending afterwards is what a fresh connection shows. That also holds when it stops sharing in the instant between the replay's read and its write: the handler of its new announcement waits for the replay and expires what it wrote.
  - A device paused or removed in the same change, and a folder ignored for the device, are not replayed.
  - Without the patch the same tests fail at their first check.

**Rejected alternatives.**
- *Reconnecting only the devices that announced the folder, outside the redial budget* (the second shape #341 named). It keeps every cost of a close that #339's review found except the budget:
  - the close message lost behind an unacknowledged write (the engine aborts the socket), and a peer that then refuses the redial until its ping notices;
  - the double dial, and the surplus close of #185;
  - the other vaults stopped for each removal;
  - over a relay, a new join after TCP and QUIC failed.

  A redial that is not counted also lets a loop of removals churn a connection past the engine's guard.
- *Closing the connections to the folder's devices* (#339). The same costs, plus the budget.
- *The bridge or the app making the entry up.* It could come from `remoteFolderStates`, which holds a state and no label, or from the removed folder's configuration. Either way, our memory of the device's announcement would take the place of the announcement, and the index start info would still be missing.
- *Keeping the removed folder's pending entries in `cleanPending`.* Those entries belong only to devices the folder was not shared with. The devices it was shared with have no entry, because an entry goes once the folder is shared with that device.
- *Asking the device for its cluster configuration.* BEP has no such message, and a stock peer sends one only on a new connection or when its own configuration changes.
- *Pausing and resuming the device, stopping and starting the engine (#302), keeping the texts.* Rejected in #316 and #339.

**Not covered.**
- **A device that is not connected at the removal** offers the folder when it next connects, as before. If the vault was accepted again before then, it is shared only with the devices that were connected. The later offer then fails in the automatic pass with "folder already exists". That chain predates this change.
- **A label the device changed without sending a cluster configuration** comes back with the label of the last one, as the engine's own pending entry already does.
- **A removal still waiting for the five-second save** is lost if the app ends within those seconds (075, #151). Once the texts no longer ask anyone to close the app, only iOS ends it there.
- **The engine's test hooks.** Two hooks, nil in the app, let a test put a newer announcement into the gap between the replay's read and its write (`TestHookReplayRead`, `TestHookAnnouncementsLocking`). They carry no behaviour of their own.

**Links.** #341, #316, #339, #340, #343, #185, #151; decisions 002, 006, 007, 008, 042, 075; `go/patches/syncthing/009-…`, `TestIssue341_…` in `go/bridge`, `Issue341RemovedShareTests`.
