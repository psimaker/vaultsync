# 083 — The desktop agent says when a newer release is out, and installs nothing

**Context.** The setup link installs the newest stable desktop release (`latest_stable_tag` in `hub/scripts/setup.sh`). After that a computer stays on its version until someone runs the link again, so a fix reaches only the people who happen to look (#303). Nothing on the desktop asks today. The engine's own upgrade check is off (046 (2)), and the agent's one download is its pinned Syncthing. This is the first request the agent makes by itself, so what it sends is decided before its code.

**Decision.**
1. **The source is the repository's public list of releases:** `GET https://api.github.com/repos/psimaker/vaultsync/releases?per_page=100`, unauthenticated. The setup link reads the same list first. A newer release is one the setup link would install: a tag `desktop-vX.Y.Z` with no suffix, neither draft nor pre-release, and greater than the running build, compared as three numbers. The greatest one is named, and the list's order decides nothing.
   - A release candidate never counts: its tag has a suffix, and its release is a draft (`desktop-release.yml`).
   - A tag whose release is not published yet, because its run waits for signing or failed, is not in the list. A release is all or nothing (052).
   - To withdraw a release, delete it or mark it a pre-release. The setup link drops it, and so does every agent at its next successful ask.
   - Only a release build asks: one whose `version` is three numbers. `dev`, `dev-<commit>` and `X.Y.Z-rc.N` make no request and say nothing.
2. **One request, with nothing that identifies the install.** It is a fixed GET with `User-Agent: vaultsync` and `Accept: application/vnd.github+json`, beside the `Host` and `Accept-Encoding` that Go's client adds. It carries no version, no system, no device ID, no account and no cookie, and it sends back nothing the server handed out (no conditional header). Each ask opens a fresh connection and follows no redirect. The comparison is made on this computer. GitHub sees the computer's address, the time and that fixed request. A network on the way sees a connection to api.github.com. Nothing reaches the project: GitHub tells a repository's owner nothing of who reads its list, and the check downloads no file, whose downloads GitHub counts. `PRIVACY.md` says:
   > VaultSync for Mac and Linux asks GitHub for the public list of VaultSync's releases an hour after its background service starts, and then once a day, to say in `vaultsync status` and in the menu when a newer release is out. The request is the same from every computer: it carries no device ID, version, account, cookie or anything about your vaults, and GitHub sees your computer's IP address and the time. Nothing is downloaded or installed by it. `vaultsync release-check off` turns it off.
3. **When.** The running agent that serves the control socket asks. A parked run does not. It asks an hour after it starts, then every 24 hours while it runs, by the wall clock so that sleep counts, and never twice within an hour. The answer is kept in memory only, and a new start asks again after its own hour. A service that restarts every few seconds asks nothing. Someone who has just run setup has that hour to turn the check off.
4. **A failure costs nothing.** These count as a failed ask: no answer within 30 s, a status other than 200 (GitHub's 60 requests an hour per address included), a redirect, a body over 8 MiB, or a body that does not read as the list. A failed ask is not retried: the next ask is the next day's. The notice of the last successful ask stands. `status` and the menu say nothing of the failure, and one fixed line goes to the service log. The ask runs beside the engine and holds no lock: sync, pause, pairing and stop never wait for it, and a stop cancels it.
5. **The off switch is a file of its own:** `release-check-off` in VaultSync's folder. `vaultsync release-check off` writes it, `vaultsync release-check on` removes it, and `vaultsync release-check` says which holds. Anything at that name counts as off: a file, a folder, a link, or an entry that cannot be looked at. Its content is never read. The agent looks before each ask and each status answer, so a change needs no restart.
   - Not in `agent.json`: an older build of the same format drops a field it does not know at its next write, and raising the format for a setting would make every older build refuse to run here (054).
   - Older builds have no check and never look at the file. Their `uninstall --remove-data` leaves VaultSync's folder with this one file in it, named as holding files VaultSync did not make. From this build on, the file is in the removal's list.
6. **Where it is said, and what.**
   - `vaultsync setup`, at its end, where it sets a computer up: one line saying that the background service looks for newer releases once a day, and that `vaultsync release-check off` turns that off. So the first run tells people before the first ask.
   - `vaultsync status`, as its last line: the newer release, the one running, and the way to update, which is to run the setup link again. A terminal whose own build is that release or newer leaves the line out: the update is in place, and status's other notes say what the service runs.
   - With the check on, nothing more. No line says "up to date", because a failed ask says nothing either. With it off, one line says that newer releases are not looked for, and that `vaultsync release-check on` looks again.
   - The menu: one line below what syncs, in the agent's words, with the setup link in its tooltip (050 (3)). The icon and the headline stay as sync makes them: no badge, no dialog, no notification. With the check off, the menu shows nothing of it.
   - Both get it as an optional `release` object in `GET /v1/status`: the version, the line and the remedy. An older menu or terminal ignores the object. An older agent sends none, and a newer menu then shows nothing.
   - Nothing is downloaded, installed or opened by itself. The way to update stays the setup link, which asks and checks as it always has.

**Why.**
- A fix reaches people only when they run the setup link. Telling them is the least that brings it, and they still decide.
- The setup link already reads this list from this host. So the notice names what the link would install, and the ask adds no party the person has not met.
- The hour's delay, the day's spacing and no retries keep it near one request a day per computer, far inside the limit GitHub gives each address, which the setup link shares.

**Rejected alternatives.**
- *Updating by itself.* It would replace the program under a running service, and with a new pin the engine too (054, 078). It would skip the checks only the setup link makes (053 (6)), and it would bring a bad release to every computer at once, with no one there.
- *A check through a server of the project's own.* That server would see every computer's address once a day, a count of installs, which VaultSync does not keep. It would also be one more service to run.
- *Asking at every start.* A service started again every 10 s would ask every 10 s, and a login would ask before anyone could turn the check off.
- *Asking in `vaultsync status`, or from the menu.* Status would wait on the network and ask at every look. The menu decides nothing and asks only the agent (048), and a Linux computer may run no menu.
- *GitHub's "latest" release.* Latest is the iPhone app's: `desktop-release.yml` passes `--latest=false`.
- *Tags, or the releases feed.* A tag is pushed before its release exists, and stays after a candidate's draft is deleted. The feed has no pre-release flag and only ten entries.
- *Sending the version, so that a server answers "newer".* It would tell which version every computer runs.
- *Keeping the last ask on disk, for one a day across restarts.* One more file and format, for what the hour's delay already bounds.
- *The switch as a flag in the service definition, or as an environment variable.* An older build's `run` refuses a flag it does not know, so its service would not start. The service's environment is not the shell's (049).

**Not covered.**
- Released builds 0.1.0 to 0.3.0 never ask. Only computers on the release that carries this, or a later one, hear of later releases.
- The way to update is the setup link. Until #380 is fixed, it asks for a pairing code on a computer already set up, so the notice ships no earlier than that fix.
- Only the newest 100 releases are read. A desktop release older than all of them is not found, and nothing is said. The setup link reads every page.
- A renamed repository ends the check for the builds that name the old one, because no redirect is followed.
- On a network that reaches GitHub only through a proxy the service's environment does not name, every ask fails, silently.
- A notice from the last successful ask stays until the next one succeeds, also when its release was withdrawn meanwhile.

**Links.** #303, #380, #267; decisions 046, 048, 049, 050, 052, 053, 054, 078.
