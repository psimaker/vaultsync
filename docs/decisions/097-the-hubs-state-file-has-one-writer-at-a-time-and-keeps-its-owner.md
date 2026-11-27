# 097 — The Hub's state file has one writer at a time and keeps its owner

**Context.** The Hub keeps its pairing state in one file: the open code (as its password scalar), the count of wrong attempts on it (the lockout of decision 036) and the devices it paired. Two processes change it. The server (`vaultsync-hub serve`) does so when it records a pairing or counts a wrong attempt; a command does so when the operator runs it (`vaultsync-hub code`, and later `device remove`, decision 094). Each change is a fresh read, the change, then a temp file and a rename, ordered by a mutex that exists only inside one process. When two processes changed the file at the same moment, the later rename undid the other's change: a new code could be replaced by the old one, a recorded pairing could vanish, a failed attempt's count could be lost. Two stores on one file lost about half of 150 to 300 changes each (#490). A command run as root (`docker compose exec -u 0`, or a program on the host) also left the file owned by root and readable by root alone, so the server answered every pairing with "hub state unavailable".

**Decision.**
1. **One lock for every writer.** Every read-modify-write of the state file, in whichever process, holds an exclusive `flock(2)` on a lock file beside it (`<state>.lock`) from its read to its rename, inside the in-process mutex. Readers take no lock: a rename always shows a whole file. Every later writer of the file goes through `stateStore.update`, or takes the same lock the same way.
2. **The lock file is open to every writer.** It is empty, made 0644 past the umask, and opened read-only, since `flock` needs no write access. A lock file that root made stays usable by the service's user.
3. **The state file keeps its owner.** A write by root keeps the existing file's owner and group, and a new file gets its folder's owner. A write by any other user that would change the owner is refused, with words that name whose file it is and how to run the command as that user. The decision is one function of the effective user, the file's owner and its folder's owner (`decideStateOwner`).
4. **Fail closed.** A write that cannot get the lock within five seconds fails and says the file is busy. So does a lock file that cannot be opened or made, a filesystem without file locks, and an owner that cannot be kept. In none of these cases does the write go on without the lock, and there is no fallback.
5. **The server when a write fails.** A failed attempt is counted also when the Hub's state cannot be written. A pairing's record is written before the vault is shared: a pairing whose record cannot be written is refused and shares nothing.
6. **Windows** keeps only the in-process lock, behind a build tag. `vaultsync-hub` is built there for `pair`, which writes no Hub state; a Hub runs on Linux.

**Why.** The server re-reads the file inside every write and keeps nothing of it between requests. Once every writer holds the same lock across its read and its rename, no write can rest on a stale read. A lock on the state file itself would not hold: each write replaces that file by a rename. Owner and lock-file rules make sure the lock never shuts out the user the server runs as, which a lock meant to protect the file must not do.

**Rejected alternatives.**
- *The command asks the server to make the change.* The server has no control channel, and its only listener is the pairing port on the LAN, which must take no administrative request.
- *A lock inside the state file.* The rename replaces the file, and with it any lock on it.
- *A silent fallback to the in-process lock* when `flock` fails or waits too long. It would bring back the lost update without anyone knowing.
- *Repairing a file already left to root.* The server cannot know who should own it beyond its folder's owner; the documentation says how to give it back.

**Links.** #490, #224; decisions 036, 037, 094; `hub/state.go`, `hub/state_lock_unix.go`, `hub/pairing.go` (`countFailure`, `provision`).
