# 078 — A run keeps the engine it has when its pin cannot be installed

**Context.** On a Mac the setup link puts the new VaultSync for Mac in place before it runs the app's `setup`. When that release pins a newer Syncthing and its download fails (no network for a moment, a checksum mismatch), setup stops. The old service process keeps syncing until it is restarted. At the next start (a login, a crash, `vaultsync start`) launchd runs the new app. Its `run` found the recorded engine differing from its pin, tried the download, failed and exited. launchd started it again every 10 s (`ThrottleInterval`, and systemd's `RestartSec` likewise), with nothing syncing until the download worked (#267). With the bare agent and on Linux, the copy is replaced only once the engine is in place, so the old agent kept syncing there. The app cannot wait the same way.

**Decision.**
1. **The background service keeps the engine its record names** when its pin cannot be installed, where all of these hold:
   - The record names an older Syncthing than the pin, at or above `keepEngineFrom` (`pins.go`). A newer recorded engine is 054's case, which parks before this point.
   - The program is the very one recorded. It is hashed again after the failed attempt, and a record with no checksum keeps nothing.
   - The engine's `config.xml` is there. Without it, `prepare` would make a config with the older engine and edit it in the pin's shape, which 054 rejects.
2. **What counts as failed:**
   - It counts: a download that did not get through or did not finish, a download that did not match its checksum, a file the install could not write.
   - It does not count: a stop, where `vaultsync stop` during the download ends the run like any stopped run; and a build with no pin for this computer (`ErrNoPin`), which is no passing trouble.
3. **Where an engine is there to keep, a download that makes no progress ends:** no answer, or no byte, for `keepEngineStallWait` (30 s). No engine runs while the attempt does. Without that, the download client waits up to ten minutes on a connection that accepts and never answers, and 30 s on an address that drops packets (measured on 2026-11-27). A slow download that goes on finishes, so a slow link still installs its pin.
4. **When it keeps the engine:**
   - One fixed line in the log says why (the cause in fixed words, with no address or path) and what tries again.
   - The record stays as it is. VaultSync's copy and `agent.json`'s format follow the app's build only once this build has taken over, its pin installed. So a record in desktop-v0.1.0's format stays in it, also beside VaultSync for Mac.
   - The agent's status and the engine's start line name the engine that runs. `vaultsync status` names the installed one.
   - What installs the pin depends on what the service runs, and status says which. Where the service runs this build, its next start tries it again: `vaultsync stop`, then `vaultsync start`, on both systems. Otherwise `vaultsync setup` with this build installs it.
5. **`keepEngineFrom`** names only a version that passed the E2E with its own archive under this agent (`TestIssue267_E2EAKeptOlderEnginePairsAndSyncs`): the service keeps it, it pairs with a Hub on the pin's engine, and a note crosses each way. CI's Desktop Syncthing E2E runs it with that version's linux-amd64 archive. A bump that changes the engine's config shape or the API the agent uses raises it to the new pin, which keeps no older engine.
6. **`setup` keeps refusing** when the download fails, because a person is there to see it. So does `vaultsync run` in a terminal. Where setup runs as VaultSync for Mac's agent, its refusal names that agent to run it again, since the `vaultsync` command may still lead to an older copy. Elsewhere its words stay: the setup link's `vaultsync` lives in a folder the link removes.

**Why.**
- The older engine ran the vaults until the new app was started. It goes on doing so under the agent that owns it now, which drives it as the older agent did.
- Ending every 10 s makes nothing safer; it only stops sync.
- 046 (1), 054 and 077's fourth row are untouched: a changed program, a newer recorded engine and a missing one end or park as before.

**Rejected alternatives.**
- *Retrying the download while the run keeps the older engine.* That swaps the engine under a running agent. The next start tries again, and status says so.
- *A bound on the whole download.* A slow link would then never install its pin: every start would be cut the same way.
- *Keeping any engine the record names.* A program changed since, or one without its config, is not the engine that ran.
- *A new field in `agent.json` saying the pin failed.* The record's versions already say it, and a new field moves the format.
- *Keeping the engine in `setup` too.* A person runs setup and sees its refusal.

**Not covered.**
- Released builds 0.1.0 to 0.3.0 still end at every start where their pin cannot be installed.
- An engine kept for a long time: the next start tries again, and nothing tries in between.
- On Linux, `vaultsync start` leaves a running unit alone. `vaultsync stop`, then `vaultsync start`, is what starts it afresh.

**Links.** #267, #264, #290; decisions 046 (1), 054 (lines 3 and 9), 077 (fourth row).
