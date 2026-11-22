# 071 — The viewer's window is a program of its own on the system's web view, through Wails 3, and asks the service for every note

**Context.** #347 gives the desktop apps a window that shows the notes VaultSync syncs and edits them with one switch (decisions 072 and 073). The desktop part has no window today:
- **The agent.** A headless service, pure Go, built with `CGO_ENABLED=0` and cross-compiled on a Linux runner for every target (decision 046).
- **The menu-bar app.** `fyne.io/systray`, which uses Cocoa through cgo on macOS, built on a macOS runner, and is pure Go on Linux (decision 048). It asks the agent over a unix socket (decision 049) and asks the person through the desktop's own dialogs (decision 050).
- **On a Mac,** both programs live in `VaultSync for Mac.app` (LSUIElement), signed and notarized with rcodesign on a Linux runner (decisions 052, 053).

The window has to run on macOS 12 and later, and on Linux for amd64 and arm64. On Windows it ships in a Store package (#217). One person keeps all of it. It has to work without a network and with a screen reader. The notes it shows are untrusted input: any device in a folder's set can write any byte into them. Decision 073 puts every read and write of a note in the service.

**Decision.**

1. **A program of its own.** The viewer is a third program beside the agent and the menu-bar app. It is started from the menu-bar app's menu, or by itself.
   - **Separate from the menu-bar app:** a window toolkit and systray each run the platform's main loop, and a window that fails does not take the menu with it, as decision 048 split the menu-bar app from the service.
   - **Separate from the service:** the service stays pure Go and headless.
2. **The system's web view, through Wails 3.**
   - **The platforms' web views:** WKWebView on macOS, WebKitGTK 6.0 with GTK 4 on Linux, and WebView2 on Windows.
   - **The interface** is HTML, CSS and JavaScript built into the program.
   - **The edit mode** is CodeMirror 6, the only editor in Obsidian since 1.5.
   - **Markdown becomes HTML on the Go side,** with goldmark v1 (CommonMark 0.31.2, footnotes built in) and extensions for wikilinks, front matter and callouts, limited to what decision 072 shows.
   - **Wails** is pinned to one version and bumped as a reviewed change, like the engine's pin (decision 046). So is the page's JavaScript, through a lockfile. Three things are settled with the code: whether Node enters CI, or a built bundle is checked in, and how that code is audited.
3. **It asks the service for everything.** New paths on the control socket of decision 049 cover:
   - the folders;
   - a folder's notes;
   - a note's bytes;
   - an image a note embeds;
   - the save with its baseline (decision 073);
   - the changes to the open note.

   The service resolves every path inside the folder's root and follows no link out of it. The window has no access of its own to a vault. With the service stopped, the window says so and shows no note: it reads nothing without the service. The socket's rule stays: the same account only, checked by the kernel.
4. **Notes are untrusted.** Four measures keep a note's content inert:
   - **The page.** It runs under a Content Security Policy that allows scripts and styles only from the program itself, images only from its own scheme (served by the service), and no connections, frames or forms. CodeMirror inserts its styles while it runs. Whether they pass this policy, by a nonce or otherwise, is read before the first line of code.
   - **Rendered HTML.** It carries no script, no event handler, no `style` attribute and no outside address (decision 072, point 3).
   - **Navigation.** Every navigation that does not stay on the program's own scheme is stopped. A click on a web link hands the address (`http`, `https`, `mailto`) to the system's browser. A synced file is never handed to the system's opener (decision 072).
   - **The bridge to Go** offers list, read, save with a baseline, and open a web link, and nothing else of the program's own. Wails' own runtime may offer the page further calls, such as windows, the clipboard and dialogs. Which ones it offers, and whether each can be switched off, is read before the first line of code. One that cannot be switched off is a reason to revisit (2).

   The web view keeps no cache or storage inside a vault.
5. **What each platform's release carries.**
   - **macOS.** A third program in `VaultSync for Mac.app`, built on the macOS runner like the menu-bar app and signed and notarized with it. That changes `make-app.sh`, `sign.sh`, `verify.sh` and the bundle checks of decision 053 (`checkApp`), so the first code PR changes the signing path of decision 052. Two ways can give the window a Dock icon and a menu bar inside an LSUIElement bundle: an app bundle of its own inside VaultSync for Mac, or the viewer switching its activation policy while a window is open. Which one is measured and settled with the code.
   - **Linux.** A program built with cgo against the distribution's WebKitGTK 6.0 and GTK 4, for each architecture on a runner of that architecture or a cross toolchain. Unlike the agent and the menu-bar app it is not static, so it has a floor: the oldest C library it is built against, and WebKitGTK 6.0. A computer below that floor keeps syncing, and the menu-bar app names what the viewer needs.
   - **Windows.** In the Store package of #217, on WebView2.

**Why.**
- **One person, one language for the programs.** The interface is written once and runs in the web view each system already maintains and updates. Every program stays Go, with Go's tests, its dependency audit (govulncheck) and the CI that exists. The page is JavaScript, a second language any web view needs.
- **The editor.** A web view brings the one editor that holds Markdown text well, CodeMirror, and HTML that a screen reader can read, both without drawing text by hand.
- **Wails 3.** It is the Go toolkit that spans all three systems. It can also build an MSIX package. The Windows Store package's manifest is VaultSync's own (#217), so that is a build convenience, not a reason. 048 rejected it for a menu-bar icon as "still beta, a WebView for a status menu". Both reasons have gone: v3.0.0 has been stable since 2026-10-26, and a window that shows HTML needs a web view.
- **What Wails 3 costs.** It has had less than a month of stable releases at this writing, and it brings out a minor release every week. The way out stays open. The page is plain HTML and JavaScript, and the Go side only renders and relays, so another host for the same page replaces that thin layer and not the interface.
- **WebKitGTK 6.0** is packaged by Ubuntu 24.04 (and by 22.04 in its updates), Debian 12 and 13, Fedora 43 to 45 and Arch, and it is Wails 3's default. The 4.0 API has left the current ones, and 4.1 is the GTK 3 line Wails plans to drop.
- **The service owns the files** (decision 073). That gives the save rule one home, and on a Mac no further program needs access to the vaults.

**Rejected alternatives.**
- *Wails 2:* critical updates only, by v3's release notes; linked to WebKitGTK 4.0 by default (gone from Ubuntu 24.04, Debian 13 and Fedora 40 on); and no documented MSIX packaging, which would only be a build convenience (#217).
- *Tauri 2:* a third language and toolchain (Rust) beside Go and the page's JavaScript, which 048 also weighed. It has no MSIX packaging of its own, a build convenience only, since its program could go into a package built by hand (#217).
- *webview/webview and its Go binding:* C++, no release since 2024-09, a Go binding without a commit since 2024-08, and one window.
- *Fyne or Gio:* they draw their own text. Fyne's screen-reader support is experimental and absent on Linux, and Gio's serves Android only. Neither has an editor or a renderer close to what decision 072 needs.
- *Electron:* a Chromium in every release, 115 to 150 MiB zipped per platform, and a new major version every eight weeks with three supported.
- *A native interface per platform* (AppKit or SwiftUI, GTK, WinUI): three interfaces for one person.
- *A browser tab served on loopback:* a TCP port that any account on the computer can reach (decision 049 rejected it), and a browser whose extensions read every page and whose sync may send its history out.
- *The window inside the menu-bar app:* two main loops in one process, and a window that fails takes the menu with it.
- *The window reading the vaults itself:* the save rule would have two homes, and on a Mac a third program would ask for access to the vaults.

**Not covered.**
- The Store package and its service model on Windows (#217).
- A Flatpak on Linux. The GNOME runtimes 48 to 51 carry WebKitGTK 6.0, as read.
- A viewer on the iPhone.
- What the window looks like.

**Read, not measured.** Nothing here was built; there is no prototype. All of it was read on 2026-11-22:
- **Wails:** its releases (v3.0.0 of 2026-10-26, v3.1.0 of 2026-11-01, v2.17.1), its Linux, Windows and cross-platform build guides, and its v2 source for the WebKitGTK API.
- **Tauri:** its Microsoft Store and prerequisites pages.
- **WebView2:** Microsoft's distribution page.
- **Distributions:** the package data of Ubuntu, Debian, Fedora and Arch, and WebKitGTK's own news.
- **Electron:** its release schedule and asset sizes.
- **Fyne and Gio:** their release notes and source.
- **Obsidian:** its blog post on CodeMirror 6.
- **goldmark:** its README.

What these sources leave open:
- Wails' docs name Debian 12 and Ubuntu 22.04 as lacking WebKitGTK 6.0, which by the package data carry it (22.04 in its updates).
- Its docs say the GTK 3 tag goes in v3.1 while its v3.1.0 code still has it.
- Apple documents no statement that WKWebView content reaches VoiceOver.
- Microsoft's sentence on WebView2 as a package dependency covers App Installer, not the Store.

To measure before the first line of code:
- VoiceOver, Orca and Narrator in reading and in edit mode;
- which calls Wails' runtime offers the page, and whether each can be switched off;
- whether CodeMirror's styles pass the Content Security Policy;
- the oldest macOS and the oldest Linux the built program runs on, and its arm64 build;
- systray's and Wails 3's main loops in one process, which (1) takes from their APIs and does not try;
- the window's start time and memory on each system;
- the Dock icon and menu bar inside VaultSync for Mac;
- that no Files & Folders question comes up when the service reads for the window.

**Links.** #347, #217; decisions 046, 048, 049, 050, 052, 053; 072 ("What the viewer shows and writes …"), 073 ("The viewer saves a note only over the version it opened …").
