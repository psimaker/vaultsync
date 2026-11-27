# 079 — setup chooses the engine's API port before the engine starts

**Context.** `agent.json` records the local port VaultSync's sync engine answers on (`guiPort`). The engine's supervisor moves off that port when another program holds it as the engine starts, and records the new one. `setup` built its engine client from the port it read before the engine started. Where the engine was not running and another program had taken the recorded port, `setup` waited a minute at the old port and stopped with "the sync engine did not start", although the engine ran on the new one. This happened in two cases, both measured with the real engine:
- the engine's folder was moved aside, one of 077's ways out;
- the engine was stopped while another program took its port.

**Decision.**
1. **`setup` chooses the port before `prepare`.** It keeps the recorded port unless another program holds it, and takes a free loopback port in that case. A fresh config gets that port, and `agent.json` records it. The engine then starts where `setup` waits, and the supervisor has nothing to move. A record without a port beside a config has the config's port, and the choice holds it like a recorded one.
2. **VaultSync's own engine keeps its port.** Two locks tell it, and neither asks the port:
   - A run that holds the engine lock holds its engine's port itself, so a repeat `setup` on a running install changes nothing.
   - An engine that outlived its run holds the lock Syncthing takes on its home while it runs (`syncthing.lock`). macOS stops no child when its parent dies.

   A lock that cannot be looked at keeps the port too. `TestIssue413_E2EAnEngineLocksItsHome` fails when a Syncthing bump moves Syncthing's lock.
3. **The supervisor's move stays.** A run still moves off a port taken between two of its starts. Every reader of the port follows `agent.json`, as the control server does.
4. **The commands that ask the engine itself do so only while a Syncthing holds the engine's home** (`engineLockStateOf`). The port they ask is the one `agent.json` records then. A run records its port before it starts its engine, also a port it moved to. `vaultsync pair` always asks the engine itself, so it checks every time. `status`, `pause` and `resume` check where no agent answers.
   - **A run holds only the engine lock:** it is starting its engine and may still move its port. They say it is starting and ask nothing.
   - **Neither lock is held:** nothing of VaultSync's runs the engine. They say so, and nothing is asked.
   - **A lock cannot be looked at:** that is not known either way. They name the lock file, and nothing is asked.
   - **The home lock is held**, also by an older agent without a control socket: the engine is asked as before.
   - **`pair` waits** for a run that is starting its engine, and for a background service on its way to a run (a loaded launchd job, a live systemd unit). The wait for the engine's API shares the same ten seconds, with a few seconds left for the API at the end. A service that runs but whose engine never starts, because it is parked, is named as that.
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
- A run that holds the engine lock but has not bound its port yet, while another program holds the recorded one.
- The instant between the choice and the engine's bind.
- A service that still runs a build from before the grace: a look in the instant its run takes the engine lock refuses that run, which starts again after its throttle interval.
- An engine that outlived its run, after a later run moved the recorded port (#419): the commands ask the moved port.

**Links.** #413, #276; decision 077.
