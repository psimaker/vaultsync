# 085 — `vaultsync start` leaves a living run alone, and says "running" only on an answer

**Context.** `vaultsync start` on a Mac loaded the service's job afresh whenever launchd still had it: it enabled the job, booted it out, waited until it was gone and bootstrapped it again. That is what a stop that ran out of time needs (#175). It also met a run on its way up. On macOS 27.2 with a stalled download of the pin (#267's case), a second `start` ended the run and loaded a new one, and the download began again from its first byte. And on both systems start said "✓ VaultSync is running again." as soon as the service manager counted the run as running, which is from its first moment: on macOS 27.2 after 1 s and under systemd after 0.13 s, while no control socket existed yet (#290). launchd was measured on macOS 12.7.6 and 27.2. A job's own state in `launchctl print` is `running` only while its process lives. It is `spawn scheduled` between two spawns, `SIGTERMed` once it is booted out while its process has not ended yet, and the print fails once the job is gone. Whether a live run stays alive, `print` does not tell.

**Decision.**
1. **start leaves a living run alone.** On a Mac, after `launchctl enable`, start leaves the job as it is where all of these hold:
   - `print` shows the job's state as `running`;
   - the program launchd loaded is the one the LaunchAgent file names, or `print` names none;
   - the engine lock (`agent.lock`) is held.

   Every other state is loaded afresh as before: between two spawns, idle, on its way out, an older definition still loaded, the lock free, and a lock that cannot be looked at. On Linux nothing changes: `systemctl --user enable --now` leaves an active unit alone by itself (seen with a run on its way up).
2. **A run takes the engine lock before its install,** before anything slow such as a pin's download, and every park comes before it. Both are now load-bearing for start too. The lock tells a run on its way up or syncing from one that parks, one just spawned, or one that has given up. A later change that moves the lock after the install, or a park after the lock, changes this rule with it.
3. **"running" only on an answer.** After the start, on both systems, start waits for the agent to answer on its control socket, read afresh from `agent.json` at each look: 40 looks a quarter second apart, each ask with 10 s. An answer is the agent's report, an error in its own words, or an older agent's 404. An ask that gets no answer in its time ends the wait, as in the menu-bar app since #288. On an answer start says "running again", or "already running" where it left the run alone. Without one it refuses nothing: it says that the service runs but does not answer yet, that it may still be installing its sync engine, that `vaultsync status` says where it stands, and that `vaultsync stop`, then `vaultsync start`, ends it if it stays so. `stop` ends a run that ignores TERM too: launchd ends it about 5 s after `bootout` (measured). Beside a service of another build, start keeps its line that leaves syncing to `vaultsync status`, and says when that service does not answer yet.

**The look at the lock and an older run.** `lock.Probe` holds the lock for an instant (`flock` exclusive and non-blocking, then let go). A run of this build that takes the lock in that instant waits `engineLockGrace` (decision 079) and takes it. desktop-v0.2.0 and desktop-v0.3.0 take the engine lock once, with no grace, before their install; desktop-v0.1.0 takes it after its install. This was read from their tags, not measured. Such an older run that meets the look in that instant refuses as "already running" and exits. But then the look itself held the lock, so it found it free, and start loads that very job afresh, as it did every running job before. The run that failed is the one start replaces at once. A bootstrap starts the job without launchd's 10 s throttle: measured on macOS 27.2, a bootstrap 5 s after the previous spawn spawned within the second. The trace is one line in the older run's log. The look is made only on a Mac, and only for a job whose state is `running`. The instant itself was not measured: it lies between the older run's spawn and its lock, and no rig hits it on purpose.

**Why.**
- A run on its way up loses its progress to a restart, and gains nothing from it. A pin's download began again from zero.
- The engine lock is the one mark a run of VaultSync's makes before anything slow. launchd cannot tell a parked run from a syncing one: both are `running` with a stable pid.
- The service manager's "running" says a process exists, not that VaultSync came up. Only the agent's answer says that, and a run that ends before its socket never gives one.

**Rejected alternatives.**
- *Leaving every `running` job alone.* A run that parks, or a definition launchd loaded before a setup that could not reload it, would then stay as it is. The lock and the loaded program tell these apart.
- *A look at the lock that takes nothing.* `fcntl(F_GETLK)` is not documented to see `flock` locks on macOS; `lsof` is slow and its output would be parsed; `/proc/locks` is Linux-only, and Linux needs no look.
- *Looking only where the service runs this very build.* That narrows the instant without removing it, and ties the rule to the comparison of programs on disk, which #416 reworks.
- *A "starting" mark in `agent.json`.* It moves the format that released builds read, and the lock says the same.
- *Refusing where no agent answers.* A run on its way up is no failure, and the menu-bar app would notify on every slow start.

**Not covered.**
- A service on an older build still loads a living run afresh when its own `vaultsync start` runs. This decision holds from the release that carries it.
- A run that holds the lock and never answers is left alone. start says so and names `vaultsync stop`, then `vaultsync start`.
- A `vaultsync run` in a terminal that holds the lock while the service's job happens to show `running` is taken for the service's run.
- Overlapping starts (#290's third point), and which build a running service runs (#416).

**Links.** #290, #267, #175, #288, #413; decisions 053, 077, 078, 079.
