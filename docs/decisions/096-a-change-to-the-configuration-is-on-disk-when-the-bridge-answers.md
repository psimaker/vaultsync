# 096 — A change to the configuration is on disk when the bridge answers

**Context.** On the iPhone the bridge stands in for the sync engine's configuration API. Every change it makes goes through `commitConfigLocked`: it hands the change to the engine and waits, bounded (decision 038), for the engine's services. `config.xml`, the file the next start loads, was written only by the engine's configuration wrapper: five seconds after the first change since its last write, never while a change's services were still at work (`lib/config/wrapper.go:239-293`), and at a stop (decision 075). The engine's own configuration API writes the file after each change; the bridge did not.

An app that ended before that started from a file without the change: closed in the app switcher, or ended by iOS, neither of which stops the engine.

A removal is the case that does harm. Inside the wait the model stops the folder, takes its marker away and drops its database (`lib/model/model.go:461-499`). A start from a file that still names the folder finds it with no index. The engine takes it for a new folder, makes its marker again and syncs the directory with its peers, without the question an accept asks (rule 7, decision 007).

Reproduced (#465), in a child process killed right after the bridge answered:
- Each change was missing from the file when its call returned.
- A removed vault was configured again, with a new index, and scanned its directory.
- An accepted share was gone with its database.

A pause the launch shield makes (#45, decision 001) was lost the same way, and the shield pauses a folder only once.

**Decision.**

1. **Where.** `commitConfigLocked` writes the file with the wrapper's own `Save` as soon as the engine has taken the change, and only then waits for the services.
   - A change the engine has taken is its live configuration, not a proposal. Everything that can refuse a change does so before the wrapper takes it:
     - a full queue (`errTooManyModifications`, `wrapper.go:31`, `:233`);
     - a change that alters nothing, which the wrapper skips (`:266`);
     - the configuration's own preparation (`:307`);
     - each verifier, the model's among them (`:317`; `lib/model/model.go:2942`).
   - Then the wrapper takes the change (`w.cfg = to`, `:323`) and only after that tells its services (`:325`). Nothing takes a change back once it is taken: a service that cannot apply it only marks that a restart is needed (`:344`).
   - The bridge learns that the change was taken when `Modify` returns without an error. It learns whether the change altered the configuration by the wrapper's own test, on copies of both sides; a call whose change alters nothing writes nothing.
   - The file may lead the services, but never lag behind them. A start completes every change the file names: it starts an added folder, keeps a paused one paused, and drops the database of every folder the file does not name (`lib/syncthing/syncthing.go:200`). A file behind the services is what brought a removed vault back.
2. **Lock.** The write in the call runs under the bridge's `mu`, which the caller holds for the whole call. `Save` takes the wrapper's lock. The lock order is `mu`, then the engine's writer (point 5), then the wrapper's.
   - No stop or start runs meanwhile, so the wrapper written is the running engine's.
   - The event logger that `Save` reports to is running: only a stop or a failed start cancels it, and a stop ends the writer first.
   - The write is synchronous work and has no deadline of its own (038).
3. **Cost.** One write per call that alters the configuration.
   - Linux (btrfs): 3.3 ms median with 1 folder, 4.4 ms with 30, at most 8.9 ms (200 writes each).
   - Mac (APFS, `F_FULLFSYNC`, as on the iPhone): 9.9 ms and 11.0 ms median, at most 41 ms.
   - **Not measured on an iPhone's storage**: no device run was possible. A nearly full or busy disk takes longer.
   - A launch and a background run write nothing. The engine writes the file at a start only when it is new or its format moves (`lib/syncthing/utils.go:88-117`), and the start's own settings and migrations alter nothing after the first start. The calls that do write there act only on a finding: a path repair after a moved container, a pause of the launch shield, an accepted share.
4. **A failed write** does not change the answer. A change the engine has taken is live, and it is answered as it would be without the write: answering a failure would make the app act as if the change had not happened (the lesson of #343).
   - **It is written again.** The next change through the bridge writes the file, even one that alters nothing, until a write succeeds. The wrapper's timer writes it once more if the change armed it: it fires once, five seconds after the first unsaved change. A stop writes it too.
   - **The bridge logs one line** with the error's kind and no path or name. It goes to the process's standard error: the bridge has no log the app keeps, and the engine's own log output stays discarded.
   - **`FlushConfiguration` writes now and answers whether the file holds the configuration**: `""`, or `config.xml was not written: <kind>`. It is for a caller that must know a change is on disk before it records that the change was made, such as the launch shield's one-time pause. Its answer is how the app's log can keep a failed write. Nothing in the app calls it yet.
   - The app shows nothing new.
5. **Beyond the 30-second wait.**
   - A change the engine took within the wait was written then; a service that outlasts the wait changes nothing on disk.
   - **A change still queued when the wait ends** (#343's "not confirmed") is written when the engine takes it, by the engine's writer (`configWrites`).
     - The writer does not wait for `mu`: another call can hold `mu` for its whole wait while the model already drops a removed folder's database, and that call can answer `""` before it lets go.
     - The writer has a lock of its own and lives as long as its engine. A stop ends it before it stops the engine, after a write in progress: no late write begins once a stop has begun, and the stop's own last write comes after it (075).
     - A start that fails ends the writer with the early services.
   - **If the engine stops first**, its stop writes whatever it took (075). A change it never took never lands, and the writer waiting for it ends with the engine; the goroutine inside the engine that handed the change over is #468's.
   - **A removal answered as done after the wait**, because it landed in the instant the wait ended, is written by `RemoveFolder` before it answers `""`. Any call that answers success for a change that landed after its wait does the same.
6. **Concurrent writes.** `Save` writes the wrapper's configuration, read under the wrapper's lock, to a temporary file in the same directory. It syncs the file, renames it over `config.xml` and syncs the directory (`lib/osutil/atomic.go:36-128`).
   - A process ended during a write leaves the previous file whole.
   - Two writes of one wrapper are serialized by that lock, and the configuration only moves forward: the wrapper takes changes one at a time from its queue. So the later write holds the newer configuration, whether it is the timer's, the stop's or the bridge's.
   - An earlier engine's wrapper is never written once a later one runs: its writer has ended (5), and a start is refused until an earlier stop has made its last write (038, 075).
7. **Left on the timer on purpose.**
   - The address cache's change (`go/bridge/addresscache.go`, outside `commitConfigLocked`): a cached address that is lost costs one discovery round trip.
   - The engine's own changes.
8. **A marker left behind.** A removal written to the file before the model took the folder's marker away, and then killed, leaves the marker in the vault's directory. The next start drops the folder's database. An accept into that directory asks first (rule 7): the bridge refuses a directory that holds anything but `.obsidian` (`nonEmptyTargetError`), the marker alone included.

**Why.** The app may end at any moment after the bridge answers, and its screen already shows the change. A file that lags behind what the services did is the one order that does harm: a database dropped, and a marker taken away, for a folder the file still names.

**Rejected alternatives.**
- *Writing after the wait, as the engine's own API does.* The file then lags behind the services for as long as the slowest of them takes, and behind the answer when the wait ends first. In the reproduction the bridge answered that the vault was removed, the model had dropped its database, the file still named it, and a kill brought it back.
- *Writing the bridge's own copy of the change before handing it to the engine.* The file would name a configuration the engine may refuse.
- *A failure as the answer to a failed write:* see (4).
- *A shorter save timer* (a patch to the engine). It narrows the window without closing it, and writes for every change the engine makes.
- *Pausing a folder before removing it,* so that a lost race leaves it paused. A later resume would then merge its directory without the question, and every removal would take two changes.

**Not covered.** The model's removal runs in parallel with the bridge's write from the moment the engine takes the change. If the model drops the database before the write's rename, a kill in between still brings the vault back.
- Measured: the write finished first in 40 of 40 rounds on Linux, and in 31 of 40 on a Mac (APFS), with folders of 1 and 1000 notes.
- The measurement can be repeated: `TestIssue465_MeasureTheWriteAgainstTheModelsRemoval` and `TestIssue465_MeasureSave` in `go/bridge/issue465_measure_test.go`. They run only with `VAULTSYNC_BRIDGE_ISSUE465_MEASURE=1`.
- A lost round leaves milliseconds, sometimes tens of them. The kill has to fall inside a call that has not answered yet, before the screen shows the removal.
- What it costs when it happens: the vault comes back, as all of them did before this decision.
- The step to take if a report ever shows it: a patch to the engine's wrapper that writes the file after the verifiers and before it tells its services (`wrapper.go:323-325`), without the save event under its lock (075's hazard). The file could then never lag, for every change by anyone.

The texts that ask people to wait ten seconds before they close VaultSync (#316) stay until the release that ships this decision.

**Links.** #465, #151, #302, #343, #316, #437, #45; decisions 001, 006, 007, 038, 075; `TestIssue465_…` in `go/bridge`.
