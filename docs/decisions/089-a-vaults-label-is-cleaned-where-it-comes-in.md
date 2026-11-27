# 089 — A vault's label is cleaned where it comes in, counted in characters, and never cut

**Context.** A vault's label is set on the Hub: by its operator (`vault create`, `vault adopt`, Syncthing's own settings) or by a paired device that started the vault with `--create`. The Hub stores a name it creates as it was sent, and it resolves a selector by ID or by label without regard to case (`findVaultByLabel`). Decision 061 cleaned the names that cross a pairing, but not a label (#320). The pairing client handed labels out as they came. The desktop agent showed them in every line after the choice, wrote them into its attempt record and into the sync engine's folder, and proposed a folder named after them that kept format characters (`safeVaultName` dropped controls and separators only); `vaultsync status` and the menu-bar app read them back from there. `vaultsync-hub pair`, the Hub's `status` and `vault list` printed them. The iPhone's Add Hub sheet showed them in a vault's row and on the button, and the folder it names for the share drops only `/`, `:` and NUL. A name typed for a new vault was cut at 64 bytes by `SanitizeName`, and the check behind it counted the characters of what was left, so it never refused: a name of more than 64 bytes — 65 characters, or fewer with accented letters — became a shorter vault name on the Hub that nobody was shown (`TestIssue320_…`, red before this change). The Hub counts 64 characters, since its first release. And a row that shows a label and then a number took its direction from a right-to-left label: the number moved in front of the name.

**Decision.**

1. A label keeps what a name keeps (061). It loses control characters, format characters other than the joiners and a flag's tags, line and paragraph separators, and the spaces at either end. A label with nothing left is the vault's ID, as the Hub lists a vault without a label.
2. A vault's name — a label, and a name typed for a new vault — is counted in characters (Unicode code points) after that, and is at most 64 (`pairing.MaxVaultName`, the Hub's limit). A typed name past it is refused where it is typed: `vaultsync pair` in a terminal and with flags (which the menu-bar app sends), `vaultsync-hub pair`, the Hub itself. It is never cut. A label from a Hub is never cut either.
3. A label is cleaned where it comes into a program, by one function (`pairing.CleanLabel`):
   - the pairing client, in every answer it hands out (the vault list and the vault shared), as it cleans the Hub's name;
   - the desktop agent, where it reads a share its engine is offered;
   - `vaultsync status`, where it reads the engine's folders and the attempt record, which an earlier version wrote as a label came;
   - the Hub's `status` and `vault list`, which read the Hub's own Syncthing.
4. What is stored is what is shown: the engine's folder label (the desktop agent's, and `vaultsync-hub pair --path`'s, through `join.AcceptShare`), the attempt record, and the label the iPhone gives a folder it accepts. Nothing stored before is rewritten.
5. The Hub keeps its labels as it has them and resolves a selector as before. Before it creates a vault — from a pairing, and from `vault create` or `vault adopt` — it cleans the name, and refuses it as existing when its cleaned form is a vault's cleaned label.
6. A device names an existing vault to the Hub by its ID. The desktop agent and the iPhone do already; `vaultsync-hub pair`'s list now does too. A name a person types to choose a vault is cleaned like a label and compared with the cleaned labels, which is what the person sees; the desktop agent then sends the one match's ID. `vaultsync-hub pair --vault NAME` sends the name as typed, and the Hub resolves it.
7. A folder made from a label is named by the cleaned label. The desktop's default folder (`safeVaultName`) and the iPhone's folder for a share (`sanitizeDirectoryName`, whichever device offers it) drop what point 1 drops, before their own rules — separators, a leading dot, `.` and `..`, the desktop's cut at 64 characters — which stay as they are. A label with nothing left names the folder by the vault's ID. The Hub's own directory stays its slug. A folder that exists is never renamed.
8. In a terminal's row that shows a vault's name with words or a number after it, the name stands between two left-to-right marks (U+200E): the vault menu of `vaultsync pair`, the vault and attempt rows of `vaultsync status`, the list of `vaultsync-hub pair`, the Hub's `status` and `vault list`. The marks are added where the row is printed, never to the label: not in the engine, the attempt record, the control socket's answers or the bridge. A sentence that quotes a label keeps its words.

**Why.**

- *Where it comes in, not where it is shown.* The desktop agent alone shows a label in some twenty lines; #292 cleaned a few of them, and the rest showed it as it came. The pairing client is where the Hub's name and its error texts are cleaned already (061, 062), for the same reason: no program that shows one has to remember. The other places in point 3 read labels the client never saw.
- *Cleaning does not change which vault a name selects.* The Hub resolves as before and keeps sending its labels as it has them, so an older `vaultsync-hub pair`, which names a vault by the label it was sent, still meets it. A device names a vault from its list by the ID, which every released Hub resolves. A typed name compared with cleaned labels finds the vault it found before. Where two vaults look the same once cleaned, it refuses and asks for the ID. Where it found none, it may find the one vault that looks like what was typed. For a new vault's name, the device's check is at least as strict as the Hub's. A label the Hub matches with a cleaned name holds nothing cleaning drops, because no such character has a case to fold, so the label is its own cleaned form.
- *Characters*, because that is what a person counts, what the Hub counts and what the agent's message says ("1 to 64 characters"). *Refused, not cut*, because a name nobody was shown must not become a vault's name. *A label is not cut* because it is a name already given. Two long labels that differ at their end would show as one. And the iPhone names the share's folder by the whole label, so its sheet would name another folder than the one it makes.
- *The Hub cleans what it creates*, so new labels hold nothing a device must drop. The devices clean anyway: labels from released Hubs and from Syncthing's settings exist, and the sender of an answer is the one party a device cannot rely on (062).
- *The folder follows the label*, so that the folder made is the one named in the line beside it. The desktop showed a cleaned row beside a default folder with the format character in it. The iPhone's sheet names the destination by the same rule as the accept, and from the cleaned label: only a folder name without what hides keeps the two the same. The rules for where a folder may be are not touched (001, 007, 058).
- *Two marks in a row.* The second ends the name's direction before the number after it, so the number stays behind the name. The first gives the row its direction where a line takes it from its first letter. It is 061's mark, for 061's reason: the Linux console prints nothing for it. In a sentence the label stands in quotes among the program's own words; a mark there would draw a blank inside the quotes of every label in Terminal.
- *Builds with and without this change pair with each other.* Nothing on the wire changes, the Hub resolves as before, and a device sends an ID.

**Rejected alternatives.**

- *Cleaning in each line that shows a label* (#320's first option): some twenty lines in one program, and the next one forgets (062).
- *Handing out the label as the Hub has it beside a cleaned one*: every caller would have to choose. The one use of the first — predicting what the Hub resolves — is served at least as strictly by the cleaned label (Why).
- *Refusing such names at `createVault` only*: it covers no label from a released Hub or from Syncthing's settings.
- *Cutting a label for display*, at 64 characters or at 64 bytes: see Why. Bytes are the unit the defect came from.
- *Counting a vault's name in bytes, and saying so*: a Greek or Russian name would end at 32 characters, while the Hub takes 64.
- *One unit for every name, a Hub's and a device's included*: every released device reads a discovery answer into 256 bytes, and 64 characters can take 256 bytes beside the prefix and the port. 061's cut at 64 bytes stays for those names.
- *A mark in every line that shows a label*: see Why. *An isolate around it*: 061. *The name last in its row*: it moves every row for everyone, for a script few names use.
- *The Hub cleaning the labels it sends*: an older `vaultsync-hub pair` would send back a label the Hub does not have.

**Not covered.**

- On the iPhone, a share's row on the Sync tab shows the label the offering device sent. Only the folder's name and the label the iPhone gives the folder follow point 7 (#439).
- A device the Hub registers, and a Hub the iPhone registers, with an empty name take the name the peer announces (#440).
- The menu-bar app's own rows and its consent take their direction from a vault's name (#299). A menu beside an older agent shows labels as that agent sends them.
- A vault's name on this computer, in `vaultsync pair`'s menu, is its folder's name as it is.
- A vault whose ID holds a character point 1 drops: a device's check of what the Hub resolves could miss another vault labelled like it. The shared vault's ID is checked after the request (the desktop agent, the bridge).
- A name with no Latin letter or digit gets no directory name on the Hub and is refused there (#438).
- Characters assigned after the toolchain's Unicode tables stay (061).

**Links.** #320, #292, #319, #299, #438, #439, #440; decisions 001, 002, 007, 045, 058, 061, 062; `TestIssue320_…` in `hub/pairing`, `hub`, `desktop`, `go/bridge` and the iPhone's tests.
