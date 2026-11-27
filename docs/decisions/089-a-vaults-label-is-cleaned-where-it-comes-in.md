# 089 — A vault's label is cleaned where it comes in, counted in characters, and never cut

**Context.** A vault's label is set on the Hub: by its operator (`vault create`, `vault adopt`, Syncthing's own settings) or by a paired device that started the vault with `--create`. The Hub stores a name it creates as it was sent, and it resolves a selector by ID or by label without regard to case (`findVaultByLabel`). Decision 061 cleaned the names that cross a pairing, but not a label. #320 found:
- The pairing client handed labels out as they came.
- The desktop agent showed them in the lines after the choice and wrote them into its attempt record and the sync engine's folder. Its menu row and some lines cut them at 64 bytes, and the folder it proposed kept format characters (`safeVaultName` dropped controls and separators only).
- `vaultsync-hub pair`, the Hub's `status` and `vault list`, and the iPhone's Add Hub sheet showed them as they came.
- The iPhone's folder for a share dropped only `/`, `:` and NUL.
- A name typed for a new vault was cut at 64 bytes by `SanitizeName`, and the check behind it counted the characters of what was left, so it never refused. A name of more than 64 bytes became a shorter vault name on the Hub that nobody was shown. The Hub counts 64 characters, since its first release.
- A row that shows a label and then a number took its direction from a right-to-left label.

The rule below is #320's. It is carried out in parts; each point says whether it holds now or which part brings it, and each part updates this record when it lands.

**Decision.**

1. **What a label keeps.** A label keeps what a name keeps (061). It loses control characters, format characters other than the joiners and a flag's tags, line and paragraph separators, and the spaces at either end: `pairing.CleanLabel`, which cuts nothing. A label with nothing left is the vault's ID, as the Hub lists a vault without a label. *Holds for a name typed for a new vault. For a label from a Hub it comes with point 3.*
2. **One length, in characters.** A vault's name is counted in Unicode code points after point 1, and is at most 64 (`pairing.MaxVaultName`, the Hub's limit). A name typed for a new vault past it is refused where it is typed, never cut. *Holds in `vaultsync pair` — in a terminal, with flags, and from the menu-bar app; `vaultsync-hub pair` and the Hub counted 64 characters already.* A label from a Hub is never cut. *Comes with point 3; until then the agent's menu row, its question where a vault should go, and setup's lines (081) cut a label at 64 bytes for display.*
3. **An earlier name is still the vault.** Up to desktop-v0.3.0 the agent sent a new vault's name cut to 64 bytes (`SanitizeName`). Where no vault has the whole name, the vault under that cut form is the vault the same name made. `--create` and the terminal's question take it as found, and say under which name the Hub has it. Only that form counts, and only then. *Holds.*
4. **Where a label is cleaned:** once, where it comes into a program, by `CleanLabel`:
   - the desktop agent, where it reads a share its engine is offered — *holds*;
   - the pairing client, in every answer it hands out (the vault list and the vault shared), as it cleans the Hub's name — *comes with the client's part of #320*;
   - `vaultsync status` and setup (081), where they read the engine's folders, the shares waiting and the attempt record — *with the client's part*;
   - the Hub's `status` and `vault list` — *with the Hub's part*.
5. **What is stored is what is shown:** the engine's folder label, the attempt record, the label the iPhone gives a folder it accepts. Nothing stored before is rewritten. *Holds for a share the agent finishes later (`resumePending`). The rest comes with the client's part and, on the iPhone, with its part on folder names.*
6. **The Hub keeps its labels and resolves a selector as before** (`findVaultByLabel` stays as it is). Before it creates a vault — from a pairing, and from `vault create` or `vault adopt` — it cleans the name. It refuses one whose cleaned form is a vault's cleaned label, as existing. *With the Hub's part.*
7. **Selection.** A device names an existing vault to the Hub by its ID. *The desktop agent and the iPhone do. `vaultsync-hub pair`'s list follows with the client's part.* A name a person types to choose a vault is cleaned like a label and compared with the cleaned labels, which is what the person sees. *With the client's part; until then the agent compares it with the labels as the Hub has them, and with `--create` also with its cleaned form and its cut form (point 3).* `vaultsync-hub pair --vault NAME` sends the name as typed.
8. **A folder made from a label is named by the cleaned label.** The desktop's default folder (`safeVaultName`) and the iPhone's folder for a share (`sanitizeDirectoryName`, whichever device offers it) drop what point 1 drops. Their own rules come after and stay: separators, a leading dot, `.` and `..`, the desktop's cut at 64 characters. The folder's name is then cut, between two characters, to at most 143 bytes. A label with nothing left names the folder by the vault's ID. The Hub's own directory stays its slug. A folder that exists is never renamed. *With the iPhone's part and the client's part.*
9. **Rows.** In a terminal's row that shows a vault's name with words or a number after it, the name stands between two left-to-right marks (U+200E). The marks are added where the row is printed, never to the label. A sentence that quotes a label keeps its words. *With the rows' part.*

**Why.**

- *Where it comes in, not where it is shown.* The desktop agent alone shows a label in some twenty lines; #292 cleaned a few, and the rest kept it as it came. The pairing client is where the Hub's name and its error texts are cleaned already (061, 062), so that no program that shows one has to remember.
- *Cleaning does not change which vault a name selects.* The Hub resolves as before and keeps sending its labels as it has them, so an older `vaultsync-hub pair`, which names a vault by the label it was sent, still meets it. A device names a vault from its list by the ID, which every released Hub resolves. A typed name compared with cleaned labels finds the vault it found before. Where two vaults look the same once cleaned, it refuses and asks for the ID. For a new vault's name the device's check is at least as strict as the Hub's: a label the Hub matches with a cleaned name holds nothing cleaning drops — no such character has a case to fold — so it is its own cleaned form.
- *Characters,* because that is what a person counts, what the Hub counts and what the agent's messages say. *Refused, not cut,* because a name nobody was shown must not become a vault's name. *A label is not cut* because it is a name already given: two long labels that differ at their end would show as one.
- *An earlier name is still the vault,* because a person who repeats a command must get the vault they already have, not a refusal for the folder it syncs, nor a second vault beside it. Only the exact form an earlier agent sent counts, and only where the whole name finds nothing, so a new name is never matched by a cut. A released agent finds a vault this one starts: it searches the name as typed as well.
- *The Hub cleans what it creates,* so new labels hold nothing a device must drop. The devices clean anyway: labels from released Hubs and from Syncthing's settings exist, and the sender of an answer is the one party a device cannot rely on (062).
- *The folder follows the label,* so that the folder made is the one named beside it. The iPhone's Add Hub sheet names the destination by the same rule as the accept, from the cleaned label; only a folder name without what hides keeps the two the same. *143 bytes,* because a 64-character label can take 256. Measured: btrfs takes 255 bytes in a name, as ext4 does; APFS takes 255 UTF-16 units (765 bytes of three-byte letters, 508 of four-byte ones). eCryptfs with encrypted names takes 143 (its documented limit, not measured). The iPhone adds " (2)" and up on a clash. At 143 bytes a name in letters of up to two bytes is never cut, and the suffix stays under every limit. The rules for where a folder may be are not touched (001, 007, 058).
- *Two marks in a row:* the second ends the name's direction before the number after it, and the first gives the row its direction where a line takes it from its first letter (061's mark). In a sentence the label stands in quotes among the program's own words; a mark there would draw a blank inside the quotes in Terminal.
- *Builds with and without this pair with each other:* nothing on the wire changes, the Hub resolves as before, and a device sends an ID or the name.

**Rejected alternatives.**

- *Cleaning in each line that shows a label:* twenty lines in one program, and the next one forgets (062).
- *Handing out the label as the Hub has it beside a cleaned one:* every caller would have to choose, and the one use of the first — predicting what the Hub resolves — is served at least as strictly by the cleaned label.
- *Refusing such names at `createVault` only:* covers no label from a released Hub or from Syncthing's settings.
- *Cutting a label for display,* at 64 characters or at 64 bytes: see Why. Bytes are the unit the defect came from.
- *Matching a new name by its first 64 bytes, as before:* it would match every new name by a cut. Only the exact earlier form counts, and only where the whole name finds nothing.
- *Counting a vault's name in bytes, and saying so:* a Greek or Russian name would end at 32 characters, while the Hub takes 64.
- *One unit for every name, a Hub's and a device's included:* every released device reads a discovery answer into 256 bytes, and 64 characters can take 256 bytes beside the prefix and the port. 061's cut at 64 bytes stays for those names.
- *No bound on a folder's name:* a label of 64 four-byte characters makes a name btrfs and ext4 refuse.
- *A mark in every line that shows a label; an isolate around it* (061); *the name last in its row:* it moves every row for everyone, for a script few names use.
- *The Hub cleaning the labels it sends:* an older `vaultsync-hub pair` would send back a label the Hub does not have.

**Not covered.**

- On the iPhone, a share's row on the Sync tab shows the label the offering device sent (#439).
- A device the Hub registers, and a Hub the iPhone registers, with an empty name take the name the peer announces (#440).
- The menu-bar app's own rows and its consent take their direction from a vault's name (#299). A menu beside an older agent shows labels as that agent sends them.
- A vault's name on this computer, in `vaultsync pair`'s menu, is its folder's name as it is.
- A vault whose ID holds a character point 1 drops: a device's check of what the Hub resolves could miss another vault labelled like it. The shared vault's ID is checked after the request (the desktop agent, the bridge).
- A name with no Latin letter or digit gets no directory name on the Hub and is refused there (#438).
- Characters assigned after the toolchain's Unicode tables stay (061).

**Links.** #320, #292, #319, #299, #438, #439, #440; decisions 001, 002, 007, 045, 058, 061, 062, 081; `TestIssue320_…` and `FuzzIssue320_CleanLabel` in `hub/pairing` and `desktop`.
