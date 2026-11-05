# 057 — The menu's Start runs what the service runs, in a process group of its own

**Context.** The menu-bar app offers Start VaultSync when no agent answers (#248, decision 050). `vaultsync start` says whether the service syncs by comparing itself with the program the service runs, and a person who only uses the menu sees what it says. On a Mac the tray is itself a login job.

**Decision.** (1) Start runs the program the background service runs: the one its LaunchAgent names on a Mac, VaultSync's own copy on Linux, the only one setup gives the unit. Without one named or there, VaultSync's copy, whose start says what is wrong. Never one found on `PATH`. (2) It runs in a process group of its own, with its own time limit, not the menu's context. (3) After it says the service runs, the Start item stays disabled until something answers or the asks end, about 15 s; then the menu says the service does not answer, notifies, and offers Start again.

**Why.** Only the program the service runs can speak for it. When launchd ends a login job it signals what is left in that job's process group, so a Quit could kill a `launchctl` half-way and leave the service unloaded with nobody told; a stand-in login job showed the signal arrive and, with its own group, not arrive. A run that keeps ending before its socket counts as running for a moment each time, so "running again" alone would stand under "not running".

**Rejected alternatives.** The agent beside the app, then the copy: "beside the app" is only a folder named `MacOS`, so a bundle kept in Downloads spoke for the service. The copy, then the agent beside the app: the run replaces the copy while start still compares with it, so an old copy can say a newer service does not sync; a stale or damaged copy also hides the older-build refusal and its remedy. `PATH`: an old download or anything of that name, with no terminal to show which ran. Quit waiting for the start: up to two minutes of an app that seems stuck.

**Links.** #248; decisions 050, 053, 054.
