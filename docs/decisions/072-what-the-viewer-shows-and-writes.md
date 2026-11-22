# 072 — What the viewer shows and writes: the vaults VaultSync syncs, Markdown with Obsidian's common syntax, one note written at a time

**Context.** #347 gives the desktop apps a window that shows the notes VaultSync syncs. A note opens in reading mode, and one switch turns it into its Markdown text to edit. It is a viewer with an edit mode, not a second Obsidian. Today, reading a note or changing a line in it needs another app (#347). Most folders VaultSync syncs today are Obsidian vaults, written in Obsidian's syntax: wikilinks, embeds, callouts, properties and tags. Some are plain Markdown folders. Every write lands on every device (decision 073), and every byte the window shows comes from a synced file that another device may have written.

**Decision.**

1. **Which folders.** The window lists the folders VaultSync syncs on this computer, as the service reports them, by their names. It has no "open folder" for anything else. A folder whose directory is missing is listed with that state and not opened. A paused folder is opened as usual.
2. **Which files.**
   - **In the list.** The list shows the folder's subfolders and its notes, that is, files ending `.md`. It leaves out everything whose name begins with a dot (`.obsidian`, `.trash`, `.stfolder`, `.stversions`, `.stignore`) and the engine's temporary files.
   - **Conflict copies.** A conflict copy (`.sync-conflict-…`) is listed beside its note, marked as one, and opens read-only. Resolving a conflict stays where it is today.
   - **Ignored notes.** A note the folder's ignore patterns leave out opens read-only, because it does not sync.
   - **Other files.** Images, PDFs and other attachments are not listed. An image shows where a note embeds it.
3. **Which Markdown.**
   - **The base.** CommonMark, with GitHub's tables, task lists, strikethrough and autolinks, plus footnotes.
   - **HTML inside a note.** It is shown only within a short list of tags that carry no script, style or address: `b`, `i`, `u`, `s`, `sub`, `sup`, `kbd`, `mark`, `br`, `details`, `summary`, with no attributes. Anything else is shown as text.
4. **How much of Obsidian's syntax.**
   - **Wikilinks** (`[[Note]]`, `[[Note|alias]]`, `[[Note#Heading]]`, `[[Note#^block]]`) are links that open the note, at the heading or block.
     - **A link with a path** opens the note at that path.
     - **A bare name** opens the note of that name when exactly one exists. When more than one note has that name, the viewer lists them and the person chooses. Obsidian's help says how Obsidian writes a new link (by default the shortest path that is unique), but it gives no rule for a bare name that fits several notes, so the viewer does not guess.
     - **A link to a note that does not exist** is shown as missing and does nothing.
   - **Embeds.** Image embeds show the image (`![[photo.png]]` and `![](photo.png)`). An embedded note shows as a link that opens it in the viewer. Every other embed, such as a PDF, audio, video or any other file, shows as its name and is not opened. The viewer never hands a synced file to the system's opener, which would start whatever program the file names.
   - **Callouts** (`> [!type] Title`, folded with `+` or `-`) show their type, title and fold. An unknown type shows as a note.
   - **Properties.** The YAML front matter shows above the note as keys and values. In edit mode it is text like the rest.
   - **Tags** (`#tag` and the `tags` property) show as tags.
   - **Highlights** (`==text==`) are highlighted. **Comments** (`%%text%%`) and block IDs (`^id`) are hidden in reading mode.
   - **Shown as their source text:** math (`$…$`), Mermaid diagrams and plugin blocks such as Dataview queries.
   - **Not shown at all:** Canvas and Bases files, the graph, backlinks, themes and CSS snippets.
5. **Nothing from the network.**
   - **Images with a web address** are not fetched. They show as a link that names the address.
   - **Links in a note.**
     - A link to a note in the folder opens it in the viewer.
     - A link to any other file in the folder shows like an embed of that file.
     - A link to a web address (`http`, `https`, `mailto`) opens in the person's browser only when the person clicks it.
     - Any other kind of address, such as `file:` or an app's own scheme, is shown and not followed.
   - **The window itself** loads nothing from outside the computer, neither fonts nor scripts nor updates.
6. **Reading mode writes nothing.**
   - A task's checkbox does not toggle.
   - Nothing under `.obsidian` and no state file inside a vault is written.
   - The window's own state stays in VaultSync's own folder: the last note, the window's size, and which folders are folded in the list.
7. **Edit mode** is the note's Markdown text in a plain text editor, with Markdown coloured by its syntax, undo, and find within the note. One switch turns it on and off: a button, and Cmd-E or Ctrl-E, the shortcut Obsidian uses for the same switch. There is no live preview and no formatting toolbar. Saving follows decision 073.
8. **What it writes.**
   - **One note.** The note open in edit mode.
   - **A new note,** only when the person chooses to keep both versions (decision 073).
   - **Nothing else,** apart from the save's own temporary files (decision 073), which the engine neither scans nor sends. No note or folder is created, renamed, moved or deleted, and no attachment is written. A link to a missing note creates nothing. Creating notes is left to other apps, and can come later with its own record.
9. **A folder that is not an Obsidian vault**, one without `.obsidian`, is shown the same way. Markdown is Markdown, and Obsidian's syntax is shown wherever a note uses it. Nothing is added to turn the folder into a vault.
10. **Finding a note.** A filter on note names, across the folder. Search through the text of notes is not in the first release.

**Why.**
- **Width.** The window has to show the notes people already have, and those are mostly Obsidian's. Wikilinks and embeds shown as raw brackets would make it useless for them, while callouts and properties cost little once the links work.
- **The cut-off.** Everything past that line is what Obsidian does, and it is left to Obsidian: plugins, canvases, the graph, live preview. Each piece would be new surface that writes or runs code.
- **A plain text editor.** It is the only editor that keeps the bytes the person did not touch (decision 073, point 10). A rich-text or live-preview editor writes the Markdown out again, in its own form.
- **No network.** A fetched image tells its server when a note was opened and from which address. No note content leaves the computer, and the window counts nothing.
- **Reading mode writes nothing.** That way the one mode that writes is the one the person switched on.

**Rejected alternatives.**
- *Plain Markdown only:* most vaults would show broken links and embeds.
- *A second Obsidian* (plugins, graph, canvas, themes, live preview): surface that writes or runs third-party code, for what Obsidian already does.
- *Notes embedded inside notes:* recursion and size for a first release. A link opens the embedded note.
- *Toggling checkboxes in reading mode, as Obsidian does:* a write from the mode that promises none.
- *Remote images, or a setting to load them:* the request is the leak, and one switch would carry it for every note.
- *Any folder the person picks:* the window is for the notes VaultSync syncs. A save is done only once the engine's index holds it (decision 073), and a folder the engine does not sync has no index.
- *Creating notes, also from a missing link:* creating, renaming and deleting are where a mistake reaches every device. The first release changes only what the person opened.
- *All HTML, or HTML cleaned of scripts:* a note comes from any device in the folder's set. A short list of harmless tags is a smaller promise than a cleaner that has to catch everything.

**Not covered.**
- Resolving conflicts.
- Search through the text of notes.
- Printing and export.
- A viewer on the iPhone.
- What the window looks like.
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
- which note Obsidian opens for a bare name that fits several notes.

**Links.** #347; decisions 002, 007, 073 ("The viewer saves a note only over the version it opened …"), and the record on the viewer's window (#347).
