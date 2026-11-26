# 072 — What the viewer shows and writes: any Markdown file with the files beside it, Obsidian's common syntax, and only the file being edited

**Context.** #347 gives the desktop apps a window to read and edit Markdown files. A file opens in Preview, and one switch on top turns it into its Markdown text to edit. The window opens any Markdown file on the computer, and the notes VaultSync syncs are among them. It is a viewer with an edit mode, not a second Obsidian. Today, reading a note or changing a line in it needs another app (#347). Most folders VaultSync syncs today are Obsidian vaults, written in Obsidian's syntax: wikilinks, embeds, callouts, properties and tags. Some are plain Markdown folders, and many Markdown files lie in no vault at all, such as a README or a few notes in a folder of their own. A write to a file the engine syncs lands on every device (decision 073), while one its folder's ignore patterns leave out stays on this computer. Every byte the window shows comes from a file that another device, a download or a mail may have written.

**Decision.**

1. **Which files.** Any Markdown file the person opens, wherever it lies: a file whose name ends `.md` or `.markdown`.
   - **How a file is opened:**
     - from Finder or the file manager with Open With, or with a double click once the person has made the viewer their default for Markdown in the system's settings. The viewer declares Markdown as a type it opens and never makes itself the default (decision 071, point 5; on Windows, the package record of #217);
     - in the viewer, with Open (Cmd-O or Ctrl-O) and the system's open dialog, or by dropping a file on the window;
     - from the menu-bar app, whose Open Viewer opens the last file the viewer showed, or the open dialog when there is none;
     - from the sidebar (point 2) or a link in the open file (point 5).
   - **One window per file.** A file opened from outside the viewer opens in a window of its own. The sidebar and a link change the file in the same window. A file that is already open brings its window to the front, so no file is open twice.
   - **A change of file in edit mode** closes the open file, which saves it (decision 073, point 3). When that save meets a version that changed since it was opened, the window stays on the open file until the person has made the choice of decision 073, point 7. No edit is dropped by a change of file.
   - **Preview first.** Every file opens in Preview (point 7).
2. **The sidebar.** On the left, a sidebar the person folds open and closed lists the Markdown files in the open file's folder, by name, sorted as the system's file manager sorts them. Picking one opens it in the same window. It starts folded, and the window remembers whether the person opened it.
   - **One folder.** It lists only the open file's own folder: not the folders below it and not the one above.
   - **Left out:** names that begin with a dot, which takes in the engine's temporary files on macOS and Linux, and the engine's temporary files on Windows (`~syncthing~…`).
   - **Conflict copies.** A conflict copy (`.sync-conflict-…`) is listed beside its file, marked as one, and opens read-only. The engine deletes a conflict copy when its file conflicts again, and the iPhone's "always skip" removes every conflict copy of a file (decision 073), so an edit made there could vanish. Resolving a conflict stays where it is today.
   - **Other files.** Images, PDFs and other attachments are not listed. An image shows where a file embeds it.
   - **The list follows the folder.** A file that appears in the folder or leaves it does the same in the list.
3. **Which Markdown.**
   - **The base.** CommonMark, with GitHub's tables, task lists, strikethrough and autolinks, plus footnotes.
   - **HTML inside a note.** It is shown only within a short list of tags that carry no script, style or address: `b`, `i`, `u`, `s`, `sub`, `sup`, `kbd`, `mark`, `br`, `details`, `summary`, with no attributes. Anything else is shown as text.
4. **How much of Obsidian's syntax.** All of the following, in every file that uses it.
   - **Wikilinks** (`[[Note]]`, `[[Note|alias]]`, `[[Note#Heading]]`, `[[Note#^block]]`) are links that open the note, at the heading or block.
     - **A link with a path** opens the note at that path inside the root (point 11).
     - **A bare name** opens the note of that name in the root when exactly one exists. When more than one note has that name, the viewer lists them and the person chooses. Obsidian's help says how Obsidian writes a new link (by default the shortest path that is unique), but it gives no rule for a bare name that fits several notes, so the viewer does not guess.
     - **A link to a note that does not exist** is shown as missing and does nothing.
   - **Embeds.** Image embeds show the image (`![[photo.png]]` and `![](photo.png)`). An embedded note shows as a link that opens it in the viewer. Every other embed, such as a PDF, audio, video or any other file, shows as its name and is not opened. The viewer never hands a file to the system's opener, which would start whatever program the file names.
   - **Callouts** (`> [!type] Title`, folded with `+` or `-`) show their type, title and fold. An unknown type shows as a note.
   - **Properties.** The YAML front matter shows above the note as keys and values. In edit mode it is text like the rest.
   - **Tags** (`#tag` and the `tags` property) show as tags.
   - **Highlights** (`==text==`) are highlighted. **Comments** (`%%text%%`) and block IDs (`^id`) are hidden in Preview.
   - **Shown as their source text:** math (`$…$`), Mermaid diagrams and plugin blocks such as Dataview queries.
   - **Not shown at all:** Canvas and Bases files, the graph, backlinks, themes and CSS snippets.
5. **Nothing from the network.**
   - **Images with a web address** are not fetched. They show as a link that names the address.
   - **Links in a file.**
     - A link to a Markdown file inside the root opens it in the viewer.
     - A link to any other file inside the root shows like an embed of that file.
     - A link that leaves the root shows its address and is not followed.
     - A link to a web address (`http`, `https`, `mailto`) opens in the person's browser only when the person clicks it.
     - Any other kind of address, such as `file:` or an app's own scheme, is shown and not followed.
   - **The window itself** loads nothing from outside the computer, neither fonts nor scripts nor updates.
6. **Preview writes nothing.**
   - A task's checkbox does not toggle.
   - Nothing under `.obsidian` and no state file is written in any folder the window shows.
   - The window's own state stays in VaultSync's own folder: the last file, the window's size, and whether the sidebar is open.
7. **Preview and Edit.** One switch on top of the window changes between Preview, where every file opens, and Edit. Cmd-E or Ctrl-E, the shortcut Obsidian uses for the same switch, does the same. Edit is the file's Markdown text in a plain text editor, with Markdown coloured by its syntax, undo, and find within the file. There is no live preview and no formatting toolbar. Saving follows decision 073.
8. **What it writes.**
   - **One file per window.** The file open in Edit.
   - **A new file,** only when the person chooses to keep both versions (decision 073).
   - **Nothing else,** apart from the save's own temporary files (decision 073), which the engine neither scans nor sends. No file or folder is created, renamed, moved or deleted, and no attachment is written. A link to a missing note creates nothing. Creating files is left to other apps, and can come later with its own record.
9. **A file outside an Obsidian vault**, in a folder without `.obsidian`, is shown the same way. Markdown is Markdown, and Obsidian's syntax is shown wherever a file uses it. Nothing is added to turn a folder into a vault.
10. **Finding a file.** A filter at the top of the sidebar narrows its list by name. There is no filter across folders and no search through the text of files in the first release. The open dialog and the system's own search find the rest.
11. **The root.** Links and embeds resolve inside the open file's root. Nothing outside it loads, and no link opens anything outside it (point 5).
    - **In a vault.** The root is the nearest folder at or above the file that holds `.obsidian`, which makes it an Obsidian vault, or `.stfolder`, the marker every folder the engine syncs holds at its top. Bare names are looked up in all of it, as Obsidian does.
    - **Any other file.** The root is the file's own folder with the folders below it. A bare name is looked up in the file's own folder only.
    - **The walk upward** ends below the home folder, and at the top of the file's volume. The home folder never counts as a vault, even when it holds `.obsidian`. For a file that lies directly in it, the root is the home folder alone, without the folders below it. Neither a stray marker nor a file in the home folder makes the whole home reachable.
    - **Symbolic links** are not followed out of the root.
    - **What the window reads** is the open file, the names in its folder, and what the open file links or embeds inside the root (decision 071, point 3).

**Why.**
- **Any file.** A viewer that opens every Markdown file is one people can make their default for Markdown. One that opened only the synced folders would leave every other Markdown file to another app.
- **The sidebar.** The files beside the open one are the ones most often wanted next. A list of one folder is the size of what the file manager shows there, and it needs no vault.
- **Preview first.** Most files are opened to be read, and the mode that writes is the one the person switches on.
- **Width.** The window has to show the notes people already have, and most of those are Obsidian's. Wikilinks and embeds shown as raw brackets would make it useless for them, while callouts and properties cost little once the links work.
- **The cut-off.** Everything past that line is what Obsidian does, and it is left to Obsidian: plugins, canvases, the graph, live preview. Each piece would be new surface that writes or runs code.
- **A plain text editor.** It is the only editor that keeps the bytes the person did not touch (decision 073, point 10). A rich-text or live-preview editor writes the Markdown out again, in its own form.
- **No network.** A fetched image tells its server when a note was opened and from which address. The window itself sends no note content anywhere, and it counts nothing. A note leaves the computer only through the sync the person set up, or in a web link the person clicks.
- **Preview writes nothing.** That way the one mode that writes is the one the person switched on.
- **The root.** Links work as they do in Obsidian inside a vault, VaultSync's or not, and nothing a file names reaches past the folder it was written for.

**Rejected alternatives.**
- *Only the folders VaultSync syncs* (this record's first draft): a viewer for one kind of folder, with every other Markdown file left to another app. Decision 073 now also says when a save is done for a file that no engine syncs.
- *Making itself the default for Markdown:* that choice is the person's, and another app may hold it.
- *The folders below in the sidebar, or the whole vault as a tree:* a list of one folder stays the size of what the person sees in the file manager, and a file outside a vault has no tree to show.
- *A filter across the whole vault:* a file outside a vault has none, and the sidebar shows one folder.
- *Links resolved only in the file's own folder:* most links in a vault that VaultSync does not sync would break.
- *Links followed anywhere on the computer:* a file would decide what the window reads beyond its folder.
- *Plain Markdown only:* most vaults would show broken links and embeds.
- *A second Obsidian* (plugins, graph, canvas, themes, live preview): surface that writes or runs third-party code, for what Obsidian already does.
- *Notes embedded inside notes:* recursion and size for a first release. A link opens the embedded note.
- *Toggling checkboxes in Preview, as Obsidian does in reading view:* a write from the mode that promises none.
- *Remote images, or a setting to load them:* the request is the leak, and one switch would carry it for every note.
- *Creating notes, also from a missing link:* creating, renaming and deleting are where a mistake reaches every device. The first release changes only what the person opened.
- *All HTML, or HTML cleaned of scripts:* a note can come from any device in a folder's set, and a file from anywhere. A short list of harmless tags is a smaller promise than a cleaner that has to catch everything.

**Not covered.**
- Resolving conflicts.
- Search through the text of files.
- Creating a new file.
- Printing and export.
- A viewer on the iPhone.
- What the window looks like: decision 071, point 6.
- The languages its texts come in.

**Read, not measured.** Obsidian's syntax was read in Obsidian's help, in its source repository `obsidianmd/obsidian-help` at commit `ced3205` of 2026-11-19. The pages:
- Obsidian Flavored Markdown;
- Internal links;
- Embed files;
- Callouts;
- Properties;
- Tags;
- Basic formatting syntax;
- HTML content, which says that Obsidian sanitizes HTML too;
- Views and editing mode, which names Ctrl+E, or Cmd+E on macOS, for the switch.

Nothing was measured against Obsidian. To measure before the first line of code:
- which syntax the vaults on the test devices actually use;
- which note Obsidian opens for a bare name that fits several notes;
- how long the lookup of a bare name takes in a large vault.

**Links.** #347; decisions 002, 007, 071 ("The viewer's window …"), 073 ("The viewer saves a note only over the version it opened …").
