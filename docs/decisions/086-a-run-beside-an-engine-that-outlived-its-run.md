# 086 — a run beside an engine that outlived its run

**Context.** VaultSync's engine is a child process of the `vaultsync run` that starts it. Under the background service, no engine outlives its run. Measured with Syncthing 2.1.7, a run ended by SIGKILL, by a crash, by a stop, or by a stop the engine cannot follow in time:
- launchd ends the job's process group;
- systemd ends the unit's control group, and on Linux the engine gets a signal when its parent dies.

On a Mac, a run outside the service — `vaultsync run` in a terminal, or `setup --no-service` — ended by SIGKILL or a crash while its terminal stays open, leaves its engine running. In one case measured it kept the engine's port and its folder for about four minutes, then stopped by itself. A run started beside such an engine:
- moved the recorded API port off it, because the port was not free (decision 079, point 3);
- started a second engine on the same folder, which stopped at once on Syncthing's lock;
- ended. The service manager started it again every ten seconds until the first engine was gone.

`status` then asked the moved port, where nothing answered, and called the engine not running. After a crash under launchd, a respawned run can meet the dying engine's lock for about a second, with the same result for one start.

**Decision.**
1. **The run keeps the recorded port only while a Syncthing is seen to hold the lock on the engine's home (`syncthing.lock`).** The run holds the engine lock, so that Syncthing is an engine an earlier run left; no Syncthing of VaultSync's starts while a run holds the engine lock. A lock that cannot be looked at keeps nothing: the run moves off a taken port, as before.
2. **It starts no engine while Syncthing is seen to hold that lock.**
   - It says so once in its log and looks again every second.
   - Once the lock is free, it starts its own engine, on the same port unless the port was taken meanwhile.
   - A lock that cannot be looked at is not waited for: the engine is started, and Syncthing's own lock decides.
   - `setup`'s engine does the same.
3. **Nothing asks the recorded port while a run has not started its own engine.** The run knows of no engine of its own until then, and it neither signals nor asks the earlier engine.
   - **Through the run's control socket,** from its first moment: `status` and the menu say the engine is starting. While the run waits for an earlier engine they say what is known without asking: an engine an earlier run left behind still holds the engine's folder and may still sync, and VaultSync starts its own once that one has ended. They give no report on the vaults in that state. `pause`, `resume` and a pairing through the agent answer that the engine is not ready yet and to try again shortly. Nothing is done to the earlier engine. The socket carries the wait as `earlierRun`, and names no Syncthing version, since this run did not start that engine.
   - **Before the socket serves** — from the engine lock until the socket, which lasts as long as a pin's download: a command whose agent does not answer looks at the locks. A run that holds the engine lock is on its way up. `status` says the engine is starting and lists the vaults as not known whether they sync. `pause` and `resume` say it is still starting and change nothing. `vaultsync pair` waits as it waits for the engine, then says the same. A run that recorded that it could not make its socket, and a run of desktop-v0.1.0, which serves none, are asked as decision 079 says. The record of a socket an earlier run could not make is removed by the next run as it takes the engine lock, so it is never taken for that run's.
   - **`vaultsync pair`** waits, as long as it waits for the engine (#413), while the run's agent says its engine is starting. Beside an engine an earlier run left it says at once that the engine is not ready yet.
   - **`setup`** asks the port only once the engine it waits for has been started: by its own supervisor, or by the run whose agent says so — the service it started, or, with `--no-service`, a `vaultsync run` that holds the engine lock.

**Why.**
- That engine is VaultSync's own and may sync this computer's vaults. Moving its port leaves `agent.json` naming a port nothing answers on.
- A second engine on the same folder can only stop, so the run waits instead of failing every ten seconds.
- The run knows of no engine of its own until it has started one, so it says what it knows and asks nothing.

**Rejected alternatives.**
- **Ending the run and leaving the restart to the service manager.** That is the loop of starts above, without the port move.
- **Asking the earlier engine to shut down through its API.** The run asks no port before it has started its own engine (point 3), and that engine stops by itself.
- **Ending it by a signal to its process.** Nothing records which process it is. Telling it by its command line is new machinery for a case that ends by itself.
- **Reporting through the earlier engine while the run waits.** For the same reason, the run asks it nothing.
- **Serving the control socket before the pin's install.** It would move the socket and its record ahead of the install, and change what `vaultsync start` sees of a run on its way up (decisions 085, 093). The locks already tell a run on its way up.
- **Holding the lock on `agent.json` until the socket serves.** Every write of `agent.json` — the run's own record of its install, a pairing, setup — would wait for a download.

**Not covered.**
- **`vaultsync stop` ends the waiting run, not the earlier engine,** which may sync on until it stops by itself.
- **A run without its control socket that waits for an earlier engine:** a command finds no agent answering and asks the recorded port as decision 079 says, where the earlier engine answers.
- **The first run of this build over a desktop-v0.1.0 install, before its socket:** `agent.json` is still in that build's format until the run has installed its pin, and a command asks as decision 079 says.
- **Beside `setup --no-service`'s own engine,** which serves no socket, `status` says the engine is starting, and `pause`, `resume` and `vaultsync pair` say it is not ready, until setup ends.
- **A run of an older build** moves the port and starts a second engine as before.

**Links.** #419; decisions 077, 079, 085, 093.
