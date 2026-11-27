# 095 — The conflict list and the conflict actions work on regular files inside the vault's own folder

**Context.** The conflict list, the comparison view's read and the actions on a conflict — keep this version, keep the other version, keep both, and the removal of an original's conflict copies when a file is skipped (`go/bridge/conflicts.go`) — work with a path relative to the vault's folder, which the app takes from the list. The path was checked as text, and the entry it named was not required to be a regular file.

**Decision.** (1) The vault's folder is opened as a root (`os.OpenRoot`), and every read, write, rename and removal of these functions is done relative to it. (2) The directories on a path are opened one at a time: each must be a directory, and the directory opened must be the one looked at. The work is then done by name in that directory, through its handle; Keep Both's rename that never replaces a file is done relative to it too. (3) The entry that is read or acted on must be a regular file, and a file that is read must be the one looked at. The original that "keep the other version" replaces must be a regular file or missing. (4) The list holds regular files only; it does not walk into a link. (5) An entry that is refused is left as it is: nothing is removed, renamed or replaced to deal with it (002). It is not listed; an action on it is refused, and no answer carries a path or a name.

**Why.** An action acts on the file the list showed, in the directory it was listed in, and on nothing else.

**Rejected alternatives.** *Resolving the path and comparing the result with the folder's path as text:* the entry can change between the comparison and its use. *Following links that stay inside the folder, as a root does by itself:* an action could then work on another file than the one that was listed, and the list never walks into a link. *Removing, renaming or replacing an entry that is refused:* recovery is the user's, never an automatic change of their files (002).

**Links.** #151; #143, #144, #184; decision 002; `TestIssue151_…` in `go/bridge`.
