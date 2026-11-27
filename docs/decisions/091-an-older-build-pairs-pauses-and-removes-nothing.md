# 091 — An older build pairs, pauses and removes nothing, and names the VaultSync set up here

**Context.** Decision 054 makes `setup`, the service's `run`, `start` and `status` of a build older than the one `agent.json` records say so and change nothing: `agent.json` names a newer Syncthing than the build's pin, or is in a newer format. `pair`, `pause`, `resume` and `uninstall` predate that guard and did not check (#281):
- An older `pair` paired through the newer engine. It took the pairing lock, wrote `agent.json` in its own shape, which drops a field it does not know, and would add the Hub and the vault to the newer engine's configuration in its own shape, behind its own older checks.
- An older `uninstall`, run from an older VaultSync for Mac, removed the service and the menu-bar icon's start at login. It put its own program over VaultSync's copy, the way back without the app (053 (4)), and wrote `agent.json` at the same format. With `--remove-data` it removed what it knew, in its own order. desktop-v0.1.0 knows no menu-bar app and leaves its start at login behind.
- Beside a newer format, `pair`, `pause` and `resume` ended on the raw format error, with no way on.
- The menu-bar app read `agent.json` in one pass and called a newer format whose fields it could not read "damaged", which a person may delete.
- `status` of an older copy that the service runs beside VaultSync for Mac said that moving the app to the Trash will not stop it, while that service parks.

**Decision.**
1. **`pair`, `pause`, `resume` and `uninstall` of an older build refuse** before anything is locked, asked or written, and before the engine or the running agent is asked. "Older" is 054's measure. The refusal says what 054's status says, that nothing was changed, and the way on.
2. **The way on names the program that holds the VaultSync set up here**, with the command to run with it:
   - First, the program the background service runs (on a Mac, the one its LaunchAgent names), then VaultSync's own copy: the first that is not this build and is the very program `agent.json` records the last setup started the service with (`started.service`).
   - Failing that, VaultSync's own copy where it is another build. Every build that takes over keeps the copy at its own: setup, and VaultSync for Mac's run and uninstall.
   - `agent.json` records no place, and a newer format is not read for one. Where nothing is found, the setup link, then the command.
   - Where the background service runs this very build, it parks, so nothing syncs. `pair`, `pause` and `resume` then name the setup link, which brings a current build to the service, and say that nothing syncs meanwhile. `uninstall` still names another program where there is one.
   - `uninstall` and `pause` also name `stop`, which writes no file and ends syncing now, except where nothing syncs.
3. **`keepCopyAtAppBuild` does nothing for an older build**: no program over the copy, no write of `agent.json`. The run parks before it and `uninstall` refuses before it; this holds for every caller.
4. **The menu-bar app reads the format first.** A newer format whose port or socket it cannot read, or that has no port where it reads one, is "older", with the setup link as the way on. A newer format whose fields read is used: the menu-bar app only reads it, and the agent that answers speaks for itself. The format has one source, `control.StateFormat`, which the agent's `agentStateVersion` is. Its value stays 2.
5. **`status` of an older build** leaves out the note that the service runs VaultSync's own copy beside the app: from desktop-v0.2.0 on, such a service parks beside the app, and the older note above it says what to do.

**Why.**
- The hazard 054 rejects is the older agent editing the newer engine in its own shape; a pairing is exactly that. Its checks are the older ones, and a later release may have fixed one.
- An older build writes nothing (054), and a field that matters raises the format only from the build that adds it.
- A removal is only as complete as the build's knowledge of what was set up. The newer build's own removal keeps its record until last (077).
- Nobody who wants to remove or pair should be sent to install something when the program that can do it is on the computer.
- Stopping fails to "nothing syncs". `stop` changes the service's state, not VaultSync's files.

**Rejected alternatives.**
- *An older `uninstall` that removes the service only.* It would leave what it does not know running, a later build's further job say, while saying syncing stopped, and would still put its copy over the newer one.
- *An older `pair` handing the pairing to the running agent over its socket.* That is a new protocol for an old client. The menu-bar app already pairs through the running agent.
- *`pause` and `resume` going on through a newer agent that answers.* They wrote nothing, but beside a newer format they ended on the raw error. One rule for the four commands, with the program that does it named, costs one command.
- *Naming the setup link always.* That sends a person to install what is already there.
- *Running a candidate's `version` to read its pin.* That runs another program from a refusal, and the record and the copy already tell.
- *A recorded place of the set-up program.* That adds a field to `agent.json`, which moves its format for released readers.

**Not covered.**
- Released builds 0.1.0 to 0.3.0. They pair, pause, resume and remove as before beside a newer setup: 0.2.0 and 0.3.0 run from the app put their copy over a newer one, and 0.1.0 leaves the menu-bar app's start at login. The guard protects only from the release that carries it on, once a later one moves the pin or the format.
- Two builds with the same pin and format. 054's measure is the engine and the format, not the release.
- A named program that is itself older and from an earlier release; one from this release on refuses in turn and names its own way on.
- `stop`, `version` and `help` are not refused: they write nothing.

**Links.** #281, #264 (PR #274), #276, #380; decisions 053, 054, 077, 085.
