# 077 — No engine is installed or started over data no record names

**Context.** `agent.json` records which Syncthing VaultSync installed, and decision 054 refuses an older pin over a newer recorded one. Without that record, setup and the service took VaultSync's folder for a fresh one. They installed their own pin over whatever program was there and kept the engine's config and database (`prepare`: "agent.json was lost"). The record could be lost to an `uninstall --remove-data` that stopped after removing it first, or in some other way. The pin has moved between released builds: 0.1.0 and 0.2.0 install 2.1.6, 0.3.0 installs 2.1.7. So an older build could put its older engine over a newer one's state, the hazard 054 forbids where the record is there (#276).

**Decision.**
1. **The state decides.** setup, run and status read three things: the record (`agent.json`: none, one naming no engine, one naming the engine), the engine's program (`bin/syncthing`, there or not, by `Lstat`), and the engine's data (`syncthing/` holding anything, or unreadable). The same table is pinned in `TestIssue276_TheStateDecidesSetupRunAndStatus`.

   | Record | Program | Data | setup | run | status |
   |---|---|---|---|---|---|
   | none, or no engine | either | none | installs, as on a fresh computer | the same | not set up |
   | none, or no engine | gone | there | refuses | refuses | the note |
   | none, or no engine | there | there | takes it back if it is this build's own engine (point 3), refuses otherwise | refuses | the note |
   | the engine | there | either | as before: a changed program is installed again (046 (1)), a missing home made anew | the same | as before |
   | the engine | gone | there | refuses | refuses | the note |
   | the engine | gone | none | sets VaultSync up anew; the computer pairs again | refuses | the note |

   - Data without a recorded engine: which engine wrote it cannot be told, and 054 says not from content either.
   - Data without its program: an engine started on it could take a new identity, or bring back notes the other devices deleted. A missing program beside data is no longer installed again. 046 (1) re-checks the program at every start and installs a changed one from the pin; the code had read a missing program as a changed one.
   - A record without program and data leaves nothing of an engine to protect. `setup`, run by someone, sets VaultSync up anew there; the service, which runs unattended, does not.
   - The rule is keyed on the recorded engine, not on the file, because a record may exist without an engine.
   - The program is "there" by `Lstat`. A removal takes the program away, and a link in its place goes as a link, so only "no entry at all" is a removal's trace. A link whose target cannot be reached, such as a disk that is not mounted, is the person's own layout. 046 (1) heals it by installing the pin over it, as before; refusing there would push someone with an unmounted disk to start over. So is a `bin` that is a link: a removal takes it away whole, so a program missing behind it is no trace of one.
2. **`run`** refuses in a terminal and parks as the service (054's pattern) in every state but the first and the fourth rows. It does so right after reading the record, after its wait for `setup.lock`, and before the engine lock. So a run that waited through a removal reads what the removal left; a record gone meanwhile is the existing stop ("removed from this computer while this run waited"). It gives back the `setup.lock` file it made, never one that was there before.
3. **`setup`** refuses before the installer. The take-back needs no engine recorded, its program and `config.xml` both there, and the program the very one the pin installs, compared with the verified download just before the rename (a hook in `install`). A failed install says the comparison needed it and changed nothing. Where only the record is left, `setup` goes on as on a fresh computer; 054's check of the recorded version still comes first. `setup` takes `setup.lock` as `pair` takes its lock, and gives back the lock file only when its own taking made it.
4. **`status`** prints the note, then the service's state:
   - not installed;
   - stopped;
   - running, but starting no sync engine here;
   - running with a sync engine that still runs (the engine lock is held), started before the state arose, which is to be stopped before the note's way out is taken.

   The usual lines' remedies ("vaultsync start resumes it") would contradict the note, and a parked service counts as running to the service manager.
5. **One note per state**, naming a way out that leads to a row where `setup` goes on:
   - data without a recorded engine: move the engine's folder aside, then run `setup`. Where `agent.json` is there, the note adds `uninstall --remove-data` to remove VaultSync instead; without it, the removal refuses the folder as not VaultSync's own.
   - data without its program: finish the removal with `uninstall --remove-data`, or move the engine's folder aside and run `setup`.
   - the record alone: finish the removal, or set VaultSync up anew with `setup`.

   Each way ends with the computer pairing with the Hub again, and vault files staying where they are. No note says `setup` keeps the engine's data.
**Why.**
- Syncthing does not promise to read state a newer version wrote (054), and without the record the agent cannot tell a newer engine from an older one.
- Stopping fails to "nothing syncs", which the other devices show and the way out undoes.

**Rejected alternatives.**
- *Comparing config versions or database content* (054).
- *A list of earlier pins' binary hashes,* so that an older engine is recognised and moved forward. That list drifts, and the case it covers, an older engine with a lost record, is rare and has the way out.
- *Letting `run` recover as `setup` does,* or letting `setup` go on over data without its program after a yes. The service runs unattended, and a yes would start an engine on such data.
- *Refusing `setup` over the record alone too.* Nothing of an engine is there to protect, and the way out of data without its program, moving the folder aside, leads to exactly that state.

**Not covered.**
- **Released builds** 0.1.0 to 0.3.0 still install over unrecorded data, and reinstall a missing program beside data. The guard protects only from the release that carries it on. Their layout and their record name the engine as this build reads them (`bin/syncthing`, `syncthing/`, `syncthing.version` since 0.1.0), and they install before they record, so an update from them meets the fourth row.
- **Recovery over an engine that is not this build's own,** an older one included, or over data without its program: the way out moves the engine's folder aside, and the computer pairs again.

**Links.** #276, #264, #175, #176; decisions 046, 053, 054.
