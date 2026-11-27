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
1. **The run keeps the recorded port while a Syncthing holds the lock on the engine's home (`syncthing.lock`), or while that lock cannot be looked at.** The run holds the engine lock, so that Syncthing is an engine an earlier run left; no Syncthing of VaultSync's starts while a run holds the engine lock. `setup` keeps the port by the same look (decision 079, point 2).
2. **It starts no engine while Syncthing holds that lock.**
   - It says so once in its log and looks again every second.
   - Once the lock is free, it starts its own engine, on the same port unless another program took it meanwhile.
   - A lock that cannot be looked at is not waited for: the engine is started, and Syncthing's own lock decides.
   - `setup`'s engine does the same.
3. **It neither signals nor asks the earlier engine.** That engine syncs on, with this computer's configuration.
4. **`status` says what is true.** The engine that answers is the one an earlier run left, it syncs, and VaultSync starts its own once it has ended. The control socket carries this as `earlierRun`. It names no Syncthing version, since this run did not start that engine.

**Why.**
- That engine is VaultSync's own and syncs this computer's vaults. Moving its port leaves `agent.json` naming a port nothing answers on.
- A second engine on the same folder can only stop, so the run waits instead of failing every ten seconds.

**Rejected alternatives.**
- **Ending the run and leaving the restart to the service manager.** That is the loop of starts above, without the port move.
- **Asking the earlier engine to shut down through its API.** That sends a request to the recorded port on the word of a lock, which says nothing about who holds the port (below).
- **Ending it by a signal to its process.** Nothing records which process it is. Telling it by its command line is new machinery for a case that ends by itself.

**Not covered.**
- **A held home lock does not prove who holds the recorded port.** It proves that a Syncthing runs on the engine's folder, not that this Syncthing holds the recorded port rather than another program. The case is narrow: a run moves off a taken port before it starts its engine, so that engine got the recorded port as it started, unless another program took the port in the instant between the run's look and the engine's bind.
- **`vaultsync stop` ends the waiting run, not the earlier engine,** which syncs on until it stops by itself.
- **A run of an older build** moves the port and starts a second engine as before.

**Links.** #419; decisions 077, 079.
