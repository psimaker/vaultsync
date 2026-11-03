# 054 — The desktop agent never installs an older Syncthing than the one set up

**Context.** The agent installs the Syncthing its `pins.go` names, and `setup` and the service's `run` install it again whenever `agent.json` records another version (decision 046). With VaultSync for Mac (decision 053) the service runs whatever build sits in Applications, and an older one can get there without any VaultSync command — a Finder drag of an old zip, a Time Machine restore; an old copy or an old download can run `setup` too. That older build would put its older Syncthing over the database and config a newer Syncthing wrote (#264).

**Decision.** `ensureEngine` (setup) and `run` refuse when `agent.json` records a newer Syncthing than the pin (`engineNewer`, vX.Y.Z, a suffix aside): nothing is changed, setup says which versions and to run the setup link, the service ends with `errOlderAgent` (said in the service log) before it touches the engine or VaultSync's copy. A pin only ever moves forward.

**Why.** Syncthing does not promise to read state a newer version wrote; an older engine may refuse it or run on it unchecked, and the agent cannot tell which. Stopping fails to "nothing syncs", which the other devices show and the setup link undoes. The guard protects only from the build that carries it on, so it ships in the first release with the app.

**Rejected alternatives.** Installing the older pin as before (the hazard). Running the newer installed engine from the older agent: the agent edits the engine's config in the shape its own pin generates, and that shape may differ (#267 keeps the installed engine only in the other direction, a newer agent whose pin could not be downloaded).

**Links.** #264, #254 (PR #260), #267; decisions 046, 053.
