# 094 — The Hub forgets a device only when its operator names it, and touches no file

**Context.** A computer that runs `vaultsync uninstall --remove-data` and is set up again pairs with a new device identity, often under the same name. The Hub keeps the old identity for good: an offline device, shared on every vault it had (#224). `vaultsync-hub status` printed names only, so the two could not be told apart, and no command removes a device; the Hub's Syncthing Web UI is the only way. Decision 037 (3) says the coordinator deletes nothing.

**Decision.**
1. **What a removal removes.** `vaultsync-hub device remove ID` removes one device from the Hub's Syncthing through the engine's own `DELETE /rest/config/devices/ID`; in that one configuration change the engine also takes the device out of every folder's device list. Then the Hub drops the device's record from its state file. Nothing else changes: no folder object is sent (037 (4) holds), no folder is removed or paused, no path changes, and no file is read, written, moved or deleted, on the Hub or on the device.
2. **In that order, and safe to repeat.** The engine's change comes first, the state file's second. A run that cannot reach the engine removes nothing. If the state file cannot be written after the engine's change, the command says that the device is gone from the Hub's Syncthing and that running it again drops the record. A run for an ID the engine no longer has but the state file still records drops that record and says so.
3. **Which devices.** Any device of the Hub's Syncthing, except:
   - the Hub itself;
   - a device connected right now — the refusal says to stop VaultSync on it first;
   - any device while one of the Hub's devices is an introducer, which could add the removed one back — the refusal says so and names the Hub's Syncthing Web UI, where that setting is made (VaultSync never sets it).

   The last device a vault is shared with may go: the confirmation names that vault and says that its files stay on the Hub, with its settings, shared with no device.
4. **Naming.** By the full device ID or its short ID — the ID's first group, as `status` and Syncthing show it — never by name. An ID that matches no device, or a short ID that matches more than one, removes nothing.
5. **Confirmation.** On a terminal the command shows the device — short ID, full ID, name, when it was last seen, its vaults — and asks for its short ID to be typed; anything else removes nothing. Without a terminal it removes only with `--confirm` and the device's full ID, which must be the device named. There is no `--yes`.
6. **`status`** shows each device's short ID first, then its name, cleaned like every name the Hub keeps, then "connected" or "offline" with when it was last seen. A row begins with what identifies it, so no name can move or break it (061).
7. **A removed device that comes back** is unknown to the Hub: its connections are refused and it is listed among the Hub's pending devices, which the coordinator never accepts. Only a new pairing code, issued on the Hub, takes it back (or the operator's own act in the Web UI). It then reconnects with the folders it has, and what changed on either side meanwhile syncs as after any absence: a note deleted on it meanwhile is deleted on the Hub too, which keeps the deleted version for 30 days (037 (5)).

**Why.**
- The removed device cannot be changed through Syncthing. The Hub sends it nothing but the end of the connection — no cluster configuration, no index — and the notes in a folder change only by its own scan or by index entries a connected peer sends. On the Hub, the engine drops only its copy of what that device announced; that makes the Hub expect less and never deletes, because a deletion travels only as a newer index entry. Read in the sources of Syncthing 1.30.0 and 2.1.7, and shown by `TestE2EIssue224_*` against both stock engines and a 1.30.0 Hub with 2.1.7 devices, a device removed while connected included.
- A removal loses no note that would otherwise be kept: a device that is gone took its unsynced changes with it either way, and one that still runs keeps them and syncs them once it pairs again. So a removal does not wait for the Hub to be up to date.
- **Beside 037.** This is a named, bounded exception to 037 (3)'s "the coordinator deletes nothing": a removal deletes exactly two entries — the device in the Hub's Syncthing, with its shares in the same change, and its record in the state file — and only on the operator's command naming the device by ID. The rest of 037 (3) holds: no folder path changes, no folder is removed, no file is deleted; so do 002 and 003.
- Refusing a connected device and asking for its short ID guard #224's own case, two devices of one name, one of them the computer in use. Files do not need them (a connected device was removed in the test without effect on any file); they keep the wrong device from being cut off.

**Rejected alternatives.**
- *Taking the device out of each folder by read-modify-write and PUT, then deleting it.* Several changes with a window between them; the engine's DELETE does both in one and sends no folder object.
- *Pausing the device instead.* It stays listed and on every vault, which is the issue.
- *Removing, or marking as stale, devices not seen for a while.* The Hub would judge what is its operator's to judge (002); a device away on a trip is not gone. `status` shows when each was last seen.
- *Naming a device by its name.* Names repeat; that is how #224 arises.
- *`--yes`.* It confirms whatever an ID matched, a mistyped short ID included.
- *Adding the removed device to the Hub's ignored devices.* A later pairing would have to undo it; a pending entry costs nothing.

**Not covered.** A device that is connected and must go now, such as a lost device that still syncs: pause it in the Hub's Syncthing Web UI, then remove it. What VaultSync on iPhone and VaultSync for Mac and Linux show when their Hub has forgotten them: the Hub as disconnected; their texts are unchanged.

**Links.** #224, #175; decisions 001, 002, 003, 036, 037, 061, 068.
