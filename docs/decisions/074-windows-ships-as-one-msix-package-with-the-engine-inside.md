# 074 — VaultSync for Windows ships as one MSIX package through the Microsoft Store, with the engine inside it

**Context.** #217 brings the desktop part to Windows through the Microsoft Store, after sync without a Hub (#325), and with the viewer (#347) from its first release. On macOS and Linux the desktop part works like this today:
- The agent downloads one pinned Syncthing and checks it against SHA-256 sums compiled into it (decision 046).
- The agent runs as a LaunchAgent or a systemd user service. The menu-bar app starts at login beside it (decisions 048, 053).
- The two talk over a unix socket in VaultSync's folder (decision 049).
- An older build never runs an older engine over a newer one's state (decision 054).
- A vault inside a cloud folder, OneDrive included, is refused (decision 046). The OneDrive roots for Windows are already listed (`desktop/cloud.go`), and so is Obsidian's registry under `%APPDATA%` (`desktop/obsidian.go`).

Decision 048 (4) planned Windows as an unsigned portable zip through winget, with a per-user Scheduled Task. #217 replaced that with the Store: a package the Store signs and updates, so nobody gets past an unknown-publisher warning and an installed copy learns of a new version by itself. The Store's rules shape the package:
- **Code at runtime.** "Your product must not attempt to fundamentally change or extend its described functionality … through any form of dynamic inclusion of code" (Store Policies 10.2.2, v7.20 of 2026-09-15). Whether downloading the pinned engine would count is our reading, not Microsoft's statement.
- **Services and elevation.** MSIX supports no per-user Windows service, and the Store takes no app that needs elevation.
- **Full trust.** A desktop program in a package runs as the user once it declares the restricted capability `runFullTrust`.

Three decisions left a question here. Decision 073 left the call that replaces a note on Windows, 071 left WebView2 in a Store package, and 072 has the viewer open any Markdown file, which a package declares in its manifest.

**Decision.**

1. **One package, full trust, signed by the Store.**
   - **What it holds:** one MSIX package per architecture (x64 and arm64), offered together as a bundle. It carries the agent, the menu-bar app, the viewer and the pinned Syncthing.
   - **How it runs:** it declares `runFullTrust` and nothing that needs elevation. The Store signs it.
   - **What there is not:** no installer of our own, no Windows service, no winget package.
   - **The manifest.** Its capabilities, startup tasks, firewall rules and file types are written for VaultSync. A tool that packages one program, such as Wails 3's MSIX packaging, builds parts of the package but does not decide them.
2. **The engine is inside the package.**
   - **At release.** The release job downloads the pinned archive, compares it with the SHA-256 in `desktop/pins.go`, and only then puts the binary into the package. The pin stays the reviewed input of decision 046.
   - **The pin's Windows entries.** `desktop/pins.go` has none yet. The pin bump that adds them gives, for each Windows archive, the archive's SHA-256 from the release's signed `sha256sum.txt.asc` (decision 046) and the SHA-256 of the binary inside it. The release job checks both.
   - **On a Windows computer the agent downloads no engine.** It runs the one in the package's install folder and compares it with the binary's sum compiled into the agent each time the service starts. A package has no install step that could record a hash. A changed binary is refused with a message, not replaced by a download.
   - **Never older.** The engine moves forward with the package. The Store serves the highest version, but a person can still install an older package by hand (`Add-AppxPackage -ForceUpdateFromAnyVersion`, which keeps the app's data). So decision 054's guard stays what protects the engine's state: an older build refuses and changes nothing.
3. **Start at sign-in through the package's own startup tasks.**
   - **Two tasks.** One `desktop:StartupTask` starts the agent without a window, and a second starts the menu-bar app. Both have `Enabled="true"`: Windows turns them on when the person first opens VaultSync after installing it.
   - **The first open.** The person opens VaultSync from Start, which opens the menu-bar app. Until the agent's task runs at the next sign-in, that app offers Start VaultSync while no agent answers (decision 057), and on Windows Start runs the agent in the package.
   - **To measure:** whether Windows also turns on the task of an entry the person never opened, and whether a task can pass `run` to the agent. If it cannot, the service gets a small program of its own.
   - **The person's switch.** The person can switch either off under Settings › Apps › Startup or in Task Manager, and VaultSync never switches one back on. That is the counterpart of respecting `launchctl disable` in decision 053.
   - **Rejected:** no immediate registration at install. It needs a capability that takes the switch away from the person.
4. **VaultSync's state lies in the package's own data folder, by its real path.**
   - **Where.** The agent keeps `agent.json`, the engine's home and database, the locks and the control socket under `%LOCALAPPDATA%\Packages\<package family name>\LocalState`.
   - **How the path travels.** The agent hands that path to every process it starts. None of them writes VaultSync's own state through a virtualized `%APPDATA%` or `%LOCALAPPDATA%` path. Windows redirects new files there for packaged programs, and whether the engine, started as a child, is redirected the same way is not documented.
   - **Vaults.** The vaults stay the person's folders, anywhere the person can write. A program in a full-trust package reads and writes them as the person does.
   - **Obsidian's registry.** It is only read, and a read of an existing file sees the real one.
   - **Not used:** `unvirtualizedResources`.
   - **Removing the app removes the state.** Removing VaultSync in Windows Settings removes this folder with it: the engine's identity (its device ID), its configuration and pairings, `agent.json`, and any unsaved draft of the viewer (decision 073). The vault folders stay. A new install is then a new device to the Hub and the other devices. On a Mac the same takes `vaultsync uninstall --remove-data`. Whether Windows removes this folder for a full-trust package is to be measured.
5. **The network.** A full-trust program needs no network capability.
   - **The ports.** The engine listens on Syncthing's sync port 22000, over TCP and over QUIC on UDP. When the person runs a Syncthing of their own, it uses a free port from 22001 to 22100 instead (`freeSyncPort` in `desktop/engine.go`, decision 046). Local discovery uses UDP 21027.
   - **The firewall rules.** The manifest declares inbound rules for those ports (`desktop2:FirewallRules`). Where they are not created, Windows asks the person the first time the engine listens.
   - **Without an inbound rule** the engine still dials out to the Hub and to other devices, and reaches them through relays. Only connections that other devices start, and local discovery's answers, fail. This is read from the engine's design, not measured.
6. **The control socket.**
   - **The socket.** It stays HTTP over an `AF_UNIX` stream socket (decision 049), in the state folder of (4). Whether that folder's path fits `AF_UNIX`'s address limit is measured. Decision 049's fallback for a folder that is too deep has no Windows form yet.
   - **Who may connect.** Access is ruled by that folder's permissions, which are the person's alone. Decision 049 also asks the kernel whose a connection is. Whether Windows lets the agent ask the same is read before code; until then the folder's permissions are the check. With the viewer the socket also reads and saves files for the window wherever the person opens one (decision 071, point 3), so the same check guards those saves.
7. **The viewer and WebView2.**
   - **Files.** The viewer reads the files it shows itself and saves through the agent (decision 071, point 3). As a full-trust program it reads and writes the person's files as the person does, like the vaults in (4).
   - **The runtime.** The viewer uses the Evergreen WebView2 runtime, which is part of Windows 11 and present on most Windows 10 computers.
   - **When it is missing.** Microsoft documents a declared dependency on the runtime only for installs through App Installer, not for the Store. So the viewer checks for the runtime when it starts, and without it says so and names Microsoft's installer. Sync does not depend on it.
   - **Rejected:** shipping a fixed runtime. It adds more than 250 MB.
8. **Saving a note on Windows (decision 073).** Windows documents no exchange of two files.
   - **The candidate.** `ReplaceFileW` with a backup name. It puts the new text in the note's place and the replaced version under the backup name, which carries the engine's temporary prefix like 073's temporary file. 073's check can hash it there, and an undo is the same call again with the displaced version as the replacement. Its documented failures leave each file under a name the check can read.
   - **Recovery needs the engine to stop with the service.** 073's recovery after a crash runs before the engine starts again, and on Windows neither of the two ways 073 names exists. Their counterpart belongs to #217's lock and process-group story, and until it stands, edit mode stays off on Windows for the notes the engine syncs. A note outside the engine's folders has no engine to come first (decision 073, point 1), so it can be edited once `ReplaceFileW` holds up.
   - **What is not documented.** Microsoft does not call it atomic. Whether another writer's rename can land between its steps is measured before the viewer's code for Windows.
   - **If it can,** decision 073 (5) applies: edit mode stays off on Windows, and the viewer only reads.
9. **Cloud folders** stay refused (decision 046). How the agent recognises them on Windows, and the guided way out of OneDrive, are on #217's list, not part of this record. One signal was read on the way: `CfGetSyncRootInfoByPath` fails for a path that is under no cloud sync root.
10. **Updates.** What a Store update does to a running package is not documented. If it closes the agent and the engine, sync stops until the person opens VaultSync again or signs in anew, because startup tasks run only at sign-in, and the other devices show this computer as disconnected.
    - **On Windows 11 24H2 and later** the package declares `uap17:UpdateWhileInUse` "defer", so an update waits for the next restart.
    - **On earlier versions** it is measured first. If the gap is real, the first release either says so or does not support them.
11. **Markdown files** (decision 072, point 1).
    - **Open with.** The package declares `.md` and `.markdown` as file types the viewer opens (`uap:FileTypeAssociation`). Explorer then lists it under Open with.
    - **Never the default.** The association sets `desktop2:AllowSilentDefaultTakeOver` to `true`. By Microsoft's page on the element, the app then will "appear in an 'Open With' list, but it won't be the default app for the file type". Its page on the registry value of that name says the same: Windows "should ignore this ProgID when determining a default handler". The person makes the viewer the default in Windows' own settings, if they want to.

This record sets up no build target and no CI job.

**Why.**
- **A package the Store signs and updates** is what #217 asks for. MSIX is the form the Store signs itself.
- **The engine inside the package.** The pin is checked once, at release, on the build machine. Every computer runs the engine the release was tested with. The engine's code comes from the package's install folder, where Windows keeps a package's programs, and not from a download written into user data. Store Policies 10.2.2 points the same way, by our reading.
- **Startup tasks** are the package's own way to start at sign-in. They leave with the package and leave the switch with the person.
- **The real data folder** keeps the agent and the engine on the same files, whatever Windows does with a child's identity.
- **The firewall rules and the WebView2 check** turn a prompt or a failure into something the package says, without needing elevation.
- **The file types** put the viewer where people open Markdown files, and leave the default with the person.

**Rejected alternatives.**
- *An unsigned portable zip through winget (048 (4)):* an unknown-publisher warning on every download, and no updates.
- *An EXE or MSI installer in the Store:* the Store takes it only signed with a code-signing certificate from a CA in Microsoft's Trusted Root Program, and from a versioned download URL we host. It signs only MSIX packages itself.
- *Downloading the engine at first start, as on macOS and Linux:* a download on every computer for what the release can check once, code written into user data, and, by our reading, Store Policies 10.2.2.
- *A per-user Scheduled Task, as #217 first planned:* it outlives an uninstall, since a package can remove it only while it runs, and it would name a program inside a folder that changes with every version.
- *A per-user Windows service, or packaged services:* MSIX does not support the first, and the second is rarely approved.
- *State under the plain `%LOCALAPPDATA%\VaultSync`:* the agent and the engine might each see a different folder through the redirection.

**Not covered.**
- The guided way out of OneDrive, and how cloud folders are recognised on Windows (#217's list).
- Unsaved viewer drafts at removal, which (4) loses with the app's folder.
- The lock and process-group story on Windows.
- The menu-bar app on Windows.
- The Store listing.
- Whether the first release supports Windows 10.

**Read, not measured.** Nothing here was built or installed; all of it was read in Microsoft's documentation, on 2026-11-22 unless another day is named:
- the Microsoft Store Policies (v7.20, 2026-09-15; 10.2.2 re-read verbatim);
- App capability declarations;
- "How packaged desktop apps run on Windows";
- Flexible virtualization;
- `desktop:StartupTask` and the StartupTask class;
- `uap17:UpdateWhileInUse`;
- `desktop2:FirewallRules`;
- `Add-AppxPackage`;
- App package requirements and submission options, and the requirements for EXE and MSI apps;
- WebView2 distribution and `win32dependencies:ExternalDependency`;
- `ReplaceFileW`, `MoveFileExW` and the TxF deprecation note;
- the cloud files API (`CfGetSyncRootInfoByPath`);
- Microsoft's post on `AF_UNIX`;
- on 2026-11-26:
  - `uap:FileTypeAssociation` with `desktop2:AllowSilentDefaultTakeOver`, in its source (`MicrosoftDocs/winrt-related`, commit `95a2ee7e`);
  - the page on ProgIDs, for the registry value of that name;
  - the page on the extensions of packaged desktop apps.

What the documentation leaves open, and what is measured before the first line of code:
- whether a child process started from the package keeps its identity and its AppData redirection;
- that a write to the package's `LocalState` by its real path is itself outside the redirection, for the agent and for the engine (4);
- whether the package's data folder is removed at uninstall for a full-trust app;
- what a Store update does to the running agent and engine, and whether they come back without a new sign-in (10);
- whether a startup task is turned on for an entry never opened, and whether it passes arguments (3);
- the socket path's length against `AF_UNIX`'s limit (6);
- whether the firewall rules are created without elevation and removed at uninstall;
- `AF_UNIX` between packaged processes, and whether the agent can learn whose a connection is;
- `ReplaceFileW` against a concurrent rename (8);
- which package of the bundle an arm64 computer takes;
- the cloud sync-root check on OneDrive, Dropbox and iCloud for Windows;
- what Windows shows the first time a Markdown file is opened after the package is installed, and that the viewer stays off the default (11).

**Links.** #217, #325, #347; decisions 046, 048 (whose (4) this replaces), 049, 053, 054, 057; 071 ("The viewer's window …"), 072 ("What the viewer shows and writes …"), 073 ("The viewer saves a note only over the version it opened …").
