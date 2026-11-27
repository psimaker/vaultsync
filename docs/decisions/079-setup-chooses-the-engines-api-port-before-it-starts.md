# 079 — setup chooses the engine's API port before the engine starts

**Context.** `agent.json` records the local port VaultSync's sync engine answers on (`guiPort`). The engine's supervisor moves off that port when another program holds it as the engine starts, and records the new one. `setup` built its engine client from the port it read before the engine started. Where the engine was not running and another program had taken the recorded port, `setup` waited a minute at the old port and stopped with "the sync engine did not start", although the engine ran on the new one. This happened in two cases, both measured with the real engine:
- the engine's folder was moved aside, one of 077's ways out;
- the engine was stopped while another program took its port.

**Decision.**
1. **`setup` chooses the port before `prepare`.** It keeps the recorded port unless another program holds it, and takes a free loopback port in that case. A fresh config gets that port, and `agent.json` records it. The engine then starts where `setup` waits, and the supervisor has nothing to move. A record without a port beside a config has the config's port, and the choice holds it like a recorded one.
2. **VaultSync's own engine keeps its port.** Two locks tell it, and neither asks the port:
   - A run that holds the engine lock holds its engine's port itself, so a repeat `setup` on a running install changes nothing.
   - An engine that outlived its run holds the lock Syncthing takes on its home while it runs (`syncthing.lock`). Under the background service none outlives its run: launchd ends the job's process group, and systemd its control group. On a Mac, a run in a terminal ended by a kill or a crash leaves its engine running until it stops by itself (decision 086).

   A lock that cannot be looked at keeps the port too. `TestIssue413_E2EAnEngineLocksItsHome` fails when a Syncthing bump moves Syncthing's lock.
3. **The supervisor's move stays.** A run still moves off a port taken between two of its starts. Every reader of the port follows `agent.json`, as the control server does.
4. **The commands that ask the engine itself do so only while a lock says an engine of VaultSync's may run**, and at the port `agent.json` records at that moment (`engineMayRunAtItsPort`). `vaultsync pair` always asks the engine itself, so it checks every time. `status`, `pause` and `resume` check where no agent answers. A command does not read the recorded port in the instant a run moves off it (#419).
   - **The engine lock held:** a run of VaultSync's owns the engine. A run leaves a port another program took as it takes that lock, before anything slow such as a pin's download (`leaveATakenPort`). `agent.json`, read then, names the port its engine answers on. Where that run's agent does not answer yet, the run is on its way up and its engine perhaps not started: nothing is asked, unless the run recorded that it could not make its control socket (decision 086).
   - **The engine lock free:** Syncthing's lock on the engine's home tells an engine that outlived its run. Syncthing tries that lock once as it starts and exits when it is taken, and a look holds it for an instant. So it is looked at only while no run holds the engine lock, when no Syncthing of VaultSync's can be starting.
   - **Neither lock held:** nothing of VaultSync's runs the engine, and nothing is asked.
   - **A lock that cannot be looked at:** that is not known either way. The lock file is named, and nothing is asked.
   - **`pair` waits** up to ten seconds for a lock and the engine's answer, under one deadline. Once a lock is seen, the engine's answer gets at least three seconds. A lock with nothing listening by then is a run still starting, and any other failure is said as the engine not answering. No lock while the background service runs is a service whose engine does not start.
   - **A run waits a quarter of a second for the engine lock** (`takeEngineLock`). A look at the lock holds it for an instant, and that must not refuse a run of this build that starts in that instant.

**Why.**
- `setup` must wait where the engine runs.
- Choosing the port before the start brings the engine and `setup` to the same port, with no step in between to follow.
- The engine lock tells a run's own engine from another program, and Syncthing's lock on its home tells an engine that outlived its run. Neither looks at the port's holder.
- Whatever answers at a port no engine of VaultSync's holds is not the engine. What a command reads there about the engine, such as that it runs or what it syncs, would be wrong.

**Rejected alternatives.**
- **`setup` follows `agent.json` while it waits.** It would still wait at the old port until the supervisor had moved, and whatever answers there is not the engine.
- **The choice in `prepare` only.** It covers a fresh folder, not an engine folder whose engine was stopped while another program took its port.
- **Keeping the recorded port and saying so.** The person cannot free a port another program holds, and the engine works on any free loopback port.
- **Asking the holder of the port whether it is this engine.** That sends a request to whatever holds the port, which this decision avoids, and the two locks tell the same.

**Not covered.**
- The instant between the choice and the engine's bind.
- A setup that read the recorded port before a run moved off it between two of its starts: it writes the port from before the move back.
- A service still on an older build. It waits for no grace, and it leaves a taken port only as it starts its engine, after its install.
- A run of an older build beside an engine that outlived its run moves the recorded port; the commands then ask the moved port. A run of this build keeps it and waits for that engine (#419, decision 086).

**Links.** #413, #276, #419; decisions 077, 086.
