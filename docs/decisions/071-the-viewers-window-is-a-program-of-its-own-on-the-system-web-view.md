# 071 — The viewer's window is a program of its own on the system's web view, through Wails 3; it reads files itself and saves through the service

**Context.** #347 gives the desktop apps a window that opens any Markdown file on the computer, the notes VaultSync syncs among them, and edits it with one switch (decisions 072 and 073). The desktop part has no window today:
- **The agent.** A headless service, pure Go, built with `CGO_ENABLED=0` and cross-compiled on a Linux runner for every target (decision 046).
- **The menu-bar app.** `fyne.io/systray`, which uses Cocoa through cgo on macOS, built on a macOS runner, and is pure Go on Linux (decision 048). It asks the agent over a unix socket (decision 049) and asks the person through the desktop's own dialogs (decision 050).
- **On a Mac,** both programs live in `VaultSync for Mac.app` (LSUIElement), signed and notarized with rcodesign on a Linux runner (decisions 052, 053).

The window has to run on macOS 12 and later, and on Linux for amd64 and arm64. On Windows it ships in a Store package (#217). One person keeps all of it. It has to work without a network and with a screen reader. The files it shows are untrusted input: any device in a folder's set can write any byte into a synced note, and any other file may come from a download or a mail. Decision 073 puts every save in the service.

**Decision.**

1. **A program of its own.** The viewer is a third program beside the agent and the menu-bar app. It starts when the person opens a Markdown file with it (decision 072, point 1), from the menu-bar app's menu, or by itself.
   - **Separate from the menu-bar app:** a window toolkit and systray each run the platform's main loop, and a window that fails does not take the menu with it, as decision 048 split the menu-bar app from the service.
   - **Separate from the service:** the service stays pure Go and headless.
2. **The system's web view, through Wails 3.**
   - **The platforms' web views:** WKWebView on macOS, WebKitGTK 6.0 with GTK 4 on Linux, and WebView2 on Windows.
   - **The interface** is HTML, CSS and JavaScript built into the program.
   - **The edit mode** is CodeMirror 6, the only editor in Obsidian since 1.5.
   - **Markdown becomes HTML on the Go side,** with goldmark v1 (CommonMark 0.31.2, footnotes built in) and extensions for wikilinks, front matter and callouts, limited to what decision 072 shows. goldmark's v2 calls itself early and asks anyone who needs an extension without v2 support to stay on v1.
   - **Wails** is pinned to one version and bumped as a reviewed change, like the engine's pin (decision 046). So is the page's JavaScript, through a lockfile. Three things are settled with the code: whether Node enters CI, or a built bundle is checked in, and how that code is audited.
3. **It reads files itself and saves through the service.**
   - **Reading.** The window reads what it shows: the open file, the names in its folder for the sidebar, and what the open file links or embeds inside its root (decision 072, point 11). Preview needs no service, so a file opens whether the service runs or not.
   - **Saving.** Every save goes through the service, in a folder the engine syncs and outside one (decision 073, point 1). New paths on the control socket of decision 049 cover:
     - entering edit mode: the service reads the file's bytes once, keeps their hash as the baseline, and hands back exactly those bytes, which the editor starts from. The window's own read for Preview never becomes the baseline;
     - the save with its baseline (decision 073);
     - the changes to the file while it is in edit mode;
     - keeping both versions (decision 073, point 7).
   - **With the service stopped,** a file still opens in Preview. The switch to edit mode says that editing needs VaultSync running and names the menu-bar app's Start (decision 057).
   - **Where the service may not write,** because the folder refuses it or the system keeps the service out (on a Mac, a Files & Folders question answered with no), the file stays in Preview and the window says why, as for a file system without the exchange (decision 073, point 5).
   - **No path from the page.** The page names no path. A path comes only from the system's open event, the open dialog, a file dropped on the window, or an entry of the sidebar's own list. Links and embeds are resolved on the Go side inside the file's root, and no link is followed out of it, a symbolic link included.
   - **Writes are not confined to a root.** The service writes the file that entered edit mode and, when the person keeps both versions, one new file beside it, and nothing else. The person chose that file, and the page cannot name another.
   - **What the socket lends.** The socket's rule stays: the same account only, checked by the kernel (decision 049).
     - **On Linux and Windows** a program of that account can read and write such a file without the service.
     - **On a Mac it may not.** The Files & Folders question is answered per program. A service let into Desktop, Documents or Downloads would read and save files there for any program of the account that asks it to. The first draft lent only the synced folders.
     - **The condition.** On a Mac the service reads and saves a file for the window only when VaultSync's own program connects, checked by its code signature. Whether macOS lets the service tell which program connects is measured before the first line of code.
     - **The way out.** If the check cannot be made, or a Mac asks the person twice for one folder (below), the window saves the files outside the synced folders itself, an alternative rejected below for its two homes of the save rule. The service then touches only the synced folders, as in the first draft. If two programs mean two questions, the way out spares the person the second question only outside the synced folders. The window reads what it opens in a synced folder in Desktop, Documents or Downloads, and the service syncs that folder, so the person is asked once for each program, with or without the way out.
4. **Files are untrusted.** Four measures keep a file's content inert:
   - **The page.** It runs under a Content Security Policy that allows scripts and styles only from the program itself, images only from its own scheme (served by the window's Go side, from inside the root), and no connections, frames or forms. CodeMirror inserts its styles while it runs. Whether they pass this policy, by a nonce or otherwise, is read before the first line of code.
   - **Rendered HTML.** It carries no script, no event handler, no `style` attribute and no outside address (decision 072, point 3).
   - **Navigation.** Every navigation that does not stay on the program's own scheme is stopped. A click on a web link hands the address (`http`, `https`, `mailto`) to the system's browser. No file a note names is handed to the system's opener (decision 072).
   - **The bridge to Go** offers the sidebar's list, reading the open file and what it links or embeds, entering edit mode, saving with a baseline, keeping both versions, the open dialog and opening a web link, and nothing else of the program's own. Wails' own runtime may offer the page further calls, such as windows, the clipboard and dialogs. Which ones it offers, and whether each can be switched off, is read before the first line of code. One that cannot be switched off is a reason to revisit (2).

   The web view keeps no cache or storage inside a folder it shows.
5. **What each platform's release carries.**
   - **macOS.** A third program in `VaultSync for Mac.app`, built on the macOS runner like the menu-bar app and signed and notarized with it. That changes `make-app.sh`, `sign.sh`, `verify.sh` and the bundle checks of decision 053 (`checkApp`), so the first code PR changes the signing path of decision 052.
     - **A Dock icon and a menu bar.** Two ways can give the window both inside an LSUIElement bundle: an app bundle of its own inside VaultSync for Mac, or the viewer switching its activation policy while a window is open.
     - **Open With leans to the first.** The document types a program opens are declared in its bundle's `Info.plist` (`CFBundleDocumentTypes`, "the document types supported by the bundle" in Apple's summary). VaultSync for Mac's executable is the menu-bar app (`desktop/macos/Info.plist`), so a file opened with that bundle would reach the menu-bar app. Two things are measured with the code, and they settle it: that macOS lists a program under Open With by these types, and whether it lists a bundle nested inside VaultSync for Mac.
     - **The Markdown type.** Apple's API names it (`UTType.markdown`, `net.daringfireball.markdown`) from macOS 27. Whether older systems know it without the viewer declaring it is measured, and the viewer declares it where they do not.
     - **Never the default.** The viewer declares the type with a rank (`LSHandlerRank`) that leaves the default to the person. Apple's page names the values Owner, Default, Alternate and None without saying what each does, so which one does that is measured.
   - **Linux.** A program built with cgo against the distribution's WebKitGTK 6.0 and GTK 4, for each architecture on a runner of that architecture or a cross toolchain. Unlike the agent and the menu-bar app it is not static, so it has a floor: the oldest C library it is built against, and WebKitGTK 6.0. A computer below that floor keeps syncing, and the menu-bar app names what the viewer needs.
     - **Open With.** Setup installs a desktop entry for the viewer that lists `text/markdown`, the type freedesktop's shared MIME database gives `*.md`, `*.mkd` and `*.markdown`, so the file manager offers it under Open With. The viewer writes no default of its own. Where no program is set as the default, the freedesktop specification lets the desktop pick its most preferred program among those that list the type, and that can be the viewer.
   - **Windows.** In the Store package of #217, on WebView2. The package declares the Markdown file types (#217's package record).
6. **What the window looks like.** Plain, and much nicer than a standard viewer. Two writing apps are its models: Apostrophe, on GNOME, and Omawrite. Nothing is taken from either: no code, no styles, no fonts and no images. The viewer's are its own.
   - **One column of text,** centred at a reading width, with wide margins and generous line spacing. Nothing else is in view but the header bar and, once the person opens it, the sidebar (decision 072, point 2).
   - **A thin header bar** holds the sidebar's button, the file's name and the switch between Preview and Edit (decision 072, point 7). It holds no formatting buttons, and there is no status bar of counters.
   - **Two faces.** Preview sets the text in a proportional face made for long reading. Edit sets the Markdown text in a monospaced face, with Markdown's marks, such as `#`, `*` and `[[`, in a quieter colour than the words.
   - **The system's settings.** Light and dark follow the system, and so does the text size. Cmd or Ctrl with plus and minus changes the size in the viewer.
   - **Colour.** Text, a background, and one accent for links and the switch. Every text clears 4.5:1 against its background in light and in dark, the floor decision 044 sets for the iPhone app.
   - **Fonts** are the system's own or a typeface bundled from its maker. Which one is chosen with the look.
   - **The place stays.** The line at the top of the window stays in view when the person switches between Preview and Edit.

   The record describes the look in words. The look itself is drawn with the code.

**Why.**
- **One person, one language for the programs.** The interface is written once and runs in the web view each system already maintains and updates. Every program stays Go, with Go's tests, its dependency audit (govulncheck) and the CI that exists. The page is JavaScript, a second language any web view needs.
- **The editor.** A web view brings the one editor that holds Markdown text well, CodeMirror, and HTML that a screen reader can read, both without drawing text by hand.
- **Wails 3.** It is the Go toolkit that spans all three systems. It can also build an MSIX package. The Windows Store package's manifest is VaultSync's own (#217), so that is a build convenience, not a reason. 048 rejected it for a menu-bar icon as "still beta, a WebView for a status menu". Both reasons have gone: v3.0.0 has been stable since 2026-10-26, and a window that shows HTML needs a web view.
- **What Wails 3 costs.** It has had a month of stable releases at this writing. Its release notes promise a minor release every week, yet v3.1.0 of 2026-11-01 was still the newest on 2026-11-26. The way out stays open. The page is plain HTML and JavaScript, and the Go side only renders and relays, so another host for the same page replaces that thin layer and not the interface.
- **WebKitGTK 6.0** is packaged by Ubuntu 24.04 (and by 22.04 in its updates), Debian 12 and 13, Fedora 43 to 45 and Arch, and it is Wails 3's default. The 4.0 API has left the current ones, and 4.1 is the GTK 3 line Wails plans to drop.
- **The service saves every file** (decision 073). The save rule and its recovery keep one home, and the service knows at every save whether the engine syncs the file. A folder that starts to sync while a file in it is in edit mode is saved under the engine's rule from that moment on.
- **The window reads for itself.** Most files are opened to be read, and Preview needs nothing of the service. A file opens whether the service runs or not.
- **The look.** A page with nothing but the text keeps the attention on reading and writing, and a program people open every day is judged first by how its text looks.

**Rejected alternatives.**
- *Wails 2:* critical updates only, by v3's release notes; linked to WebKitGTK 4.0 by default (gone from Ubuntu 24.04, Debian 13 and Fedora 40 on); and no documented MSIX packaging, which would only be a build convenience (#217).
- *Tauri 2:* a third language and toolchain (Rust) beside Go and the page's JavaScript, which 048 also weighed. It has no MSIX packaging of its own, a build convenience only, since its program could go into a package built by hand (#217).
- *webview/webview and its Go binding:* C++, no release since 2024-09, a Go binding without a commit since 2024-08, and one window.
- *Fyne or Gio:* they draw their own text. Fyne's screen-reader support is experimental and absent on Linux, and Gio's serves Android only. Neither has an editor or a renderer close to what decision 072 needs.
- *Electron:* a Chromium in every release, 115 to 150 MiB zipped per platform, and a new major version every eight weeks with three supported.
- *A native interface per platform* (AppKit or SwiftUI, GTK, WinUI): three interfaces for one person.
- *A browser tab served on loopback:* a TCP port that any account on the computer can reach (decision 049 rejected it), and a browser whose extensions read every page and whose sync may send its history out.
- *The window inside the menu-bar app:* two main loops in one process, and a window that fails takes the menu with it.
- *The service reading for the window as well* (this record's first draft): nothing would open while the service is stopped, and the service would read every file the person opens. On a Mac it would lend its reads as (3) lends its saves. It would keep the question for the person's folders with one program. Whether two programs mean two questions is measured (below).
- *The window saving the files outside the synced folders itself:* the save rule and its recovery would have two homes, and a folder that starts to sync during an edit would be saved by a program that does not know it. It would lend nothing outside the synced folders, since the service would touch only those, and it is the way out of (3) if the check there cannot be made.
- *A look like Obsidian's, with panes, a ribbon and a status bar:* a second Obsidian, which decision 072 rules out.
- *The system's plain text view alone:* what any standard viewer shows.

**Not covered.**
- The Store package and its service model on Windows (#217).
- A Flatpak on Linux. The GNOME runtimes 48 to 51 carry WebKitGTK 6.0, as read.
- A viewer on the iPhone.
- The drawn look: its colours, sizes and typeface, which come with the code.

**Read, not measured.** Nothing here was built; there is no prototype. All of it was read on 2026-11-22, except where 2026-11-26 is named:
- **Wails:** its releases (v3.0.0 of 2026-10-26, v3.1.0 of 2026-11-01, v2.17.1), its Linux, Windows and cross-platform build guides, and its v2 source for the WebKitGTK API. On 2026-11-26, its release list again: no weekly release after v3.1.0, only nightlies of v3.2.0.
- **Tauri:** its Microsoft Store and prerequisites pages.
- **WebView2:** Microsoft's distribution page.
- **Distributions:** the package data of Ubuntu, Debian, Fedora and Arch, and WebKitGTK's own news.
- **Electron:** its release schedule and asset sizes.
- **Fyne and Gio:** their release notes and source.
- **Obsidian:** its blog post on CodeMirror 6.
- **goldmark:** its README, and on 2026-11-26 the README of its v2.
- **The models, on 2026-11-26:** the READMEs and screenshots of Apostrophe (`World/apostrophe` on GNOME's GitLab, commit `7fe8f1d6`) and of Omawrite (`omacom/omawrite` on GitHub, commit `1c09d424`).
- **Opening files, on 2026-11-26:**
  - Apple's pages on `CFBundleDocumentTypes`, `LSHandlerRank` and `UTType.markdown`;
  - freedesktop's specifications of desktop entries and of how a program is chosen for a type (`mime-apps`);
  - the `text/markdown` entry of its shared MIME database (`xdg/shared-mime-info`, commit `831f1e16`).

What these sources leave open:
- Wails' docs name Debian 12 and Ubuntu 22.04 as lacking WebKitGTK 6.0, which by the package data carry it (22.04 in its updates).
- Its docs say the GTK 3 tag goes in v3.1 while its v3.1.0 code still has it.
- Apple documents no statement that WKWebView content reaches VoiceOver. Its page on `LSHandlerRank` does not say what each value does, and its page on `CFBundleDocumentTypes` gives only a one-line summary.
- Microsoft's sentence on WebView2 as a package dependency covers App Installer, not the Store.

To measure before the first line of code:
- VoiceOver, Orca and Narrator in Preview and in edit mode;
- which calls Wails' runtime offers the page, and whether each can be switched off;
- whether CodeMirror's styles pass the Content Security Policy;
- the oldest macOS and the oldest Linux the built program runs on, and its arm64 build;
- systray's and Wails 3's main loops in one process, which (1) takes from their APIs and does not try;
- the window's start time and memory on each system;
- the Dock icon and menu bar inside VaultSync for Mac, and whether macOS lists a bundle nested there under Open With;
- whether macOS before 27 knows the Markdown type, and which `LSHandlerRank` keeps the viewer from becoming the default;
- on a Mac, the Files & Folders question:
  - which program it names when the window reads a file in Desktop, Documents or Downloads, and when the service saves one there;
  - whether the person is asked twice for one folder;
  - what a save meets when the answer was no;
  - whether the service can tell which program connects to its socket.

  Two questions for one file, or a check that cannot be made, take the way out of (3).

**Links.** #347, #217; decisions 044, 046, 048, 049, 050, 052, 053, 057; 072 ("What the viewer shows and writes …"), 073 ("The viewer saves a note only over the version it opened …").
