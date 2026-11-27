# 080 — The setup link never replaces a newer VaultSync for Mac, by the version each app's agent says

**Context.** The setup link puts the newest stable `desktop-v*` release's VaultSync for Mac in place of the installed one, unless the two hold the same programs, byte for byte (decision 053 (6)). It replaced any other build, a newer one too, and removed the replaced app once the new one was in place. A release that is withdrawn after it reached a Mac (deleted, or marked a pre-release) makes the link serve the release before it. The newer app was then removed and the older one put in its place. If the newer release had moved the Syncthing pin, the older agent's setup refused to run its engine over the newer one's state (decision 054), and its service parked. Running the link again changed nothing, and the withdrawn release cannot be downloaded again (#277).

**Decision.**
1. **The measure is the release each app's agent names.** Both agents are asked `vaultsync version` before anything is set aside: the download's, which the link already runs to see that it runs on this Mac, and the installed one's. The line reads `vaultsync X.Y.Z (Syncthing A.B.C)`, with a suffix such as `-rc.1` for a release candidate. The release workflow stamps the version from the tag (`-X main.version`).
2. **How the link reads it.** X.Y.Z is compared as three numbers, a suffix set aside (as `engineVersion` does for Syncthing). Where the installed app's release is the higher, the link stops with exit 1. It changes nothing, runs no agent, and names both versions and the command for the installed app's own setup, with the arguments this run was given, each quoted for the shell. It names no way to the older release: an older agent over a newer install is what this decision prevents.
3. **The other cases:**
   - Another build of the same release (a release candidate, a rebuild), or an older release, is replaced as before.
   - An installed app whose line names no release is never replaced. That is `dev` from a local build, `dev-<commit>` from a pull request's build, or any other form. A download whose line names none replaces nothing. The link says to move the installed app to the Trash and run the link again to install the release.
   - An installed app whose agent is no plain executable file, has a link on the way to it (the app, `Contents`, `Contents/MacOS` or the agent itself), or does not run, is replaced as before: nothing runs from it, and the agent's own `checkApp` passes over such an app. An agent with a link on the way to it is never run; a link may lead anywhere.
4. **The line is a contract between the agent and the setup link.** `TestIssue277_TheVersionLineIsTheFormTheSetupLinkReads` builds the agent as the release job does and holds its line to the form `release_of` in `hub/scripts/setup.sh` reads. The test carries that rule as a copy (`setupLinkRelease`), checked on the same examples as the dry-run test. A change of the line, or of the rule, changes both in one pull request.

**Why.**
- The link's question is which of two builds of the app stays: an app against an app. Every pin bump and every new `agent.json` format ships in a higher release (decision 054: a pin only moves forward). So wherever the agent's own guard would refuse the older build, the installed release is the higher one too.
- A newer release with the same pin is kept too. It may have written `agent.json` in a newer format, which the pin cannot show.
- A line that names no release has no place in the order. Replacing it would remove a build the person made or chose, which the link cannot get back (doctrine 002).
- An unreadable download must not decide either. Should a release change its line, updates over an installed app stop until the link reads it, which the release candidate's run through the link shows.

**Rejected alternatives.**
- **The Syncthing pin, the measure of the agent's guard (`olderThanSetUp`).** It answers whether an engine may run over its state, not which app stays. Many releases share one pin, so it would replace, and remove, a newer app with the same pin.
- **The tag for the download's side.** That is a second source for one question; both apps are asked the same one.
- **Running the kept app's own setup**, as the same-build case does. This run did not verify that build, and it may come from a release withdrawn for a defect. The link names the command instead.
- **Replacing an app whose release cannot be read.** That removes a development build without asking.

**Not covered.**
- The bare agent and Linux. There the downloaded agent is only the installer, and its setup replaces VaultSync's copy only after its engine check (`ensureEngine` before `installAgentCopy`). Where the newer build moved the pin or `agent.json`'s format, the older one refuses before anything changes, and the newer copy keeps running (decision 054). Otherwise it puts an older build in place that runs on the same engine and state, so nothing parks.
- A dry run, which unpacks nothing and runs no agent. It says the app is replaced unless it is already this build, or that the setup stops and changes nothing where the installed one is a newer release or does not say which release it is.
- Something put at the app's place between this check and the set-aside (#268).

**Links.** #277, #264, #268; decisions 002, 053 (6), 054.
