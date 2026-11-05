# 056 — The socket's answer names the Hubs that answered; its words and `needs` stay

**Context.** When several Hubs answer, the agent refuses with `needs: hub`, because a code goes only to the Hub the person chose (decision 045). The terminal offers a numbered list; the menu could only ask for an address under the refusal's words (#250). A Finder drag replaces the app while the old menu-bar app runs on, so the two ends of the socket can differ.

**Decision.** `PairResult` gains `hubs` (`name`, `address`) beside the refusal. A menu that gets `hubs` offers them by name and sends the chosen one's address as `hub`, as `--hub` does; the agent checks it like any other. One that gets none asks for an address, as before. The refusal's words and `needs: hub` stay as they were, and on Linux the list of Hubs starts with a row that chooses nothing, as the list of vaults does (decision 055).

**Why.** A field an older client ignores needs no new path or version (decision 049), and each mix of versions keeps working: a v0.2.0 menu beside a new agent asks for the address under the words it knows, a new menu beside a v0.2.0 agent does the same. A Hub's name is only a hint from the network and can claim anything, so the row begins with its address, the one field an answer cannot write, without the format and separator characters that could reverse or hide it; and a row is read back by its tag or number, never its words.

**Rejected alternatives.** A new `needs` value for the list: a v0.2.0 menu would read it as a flag it cannot ask for and end the pairing. Parsing the names from the refusal (decision 050). The menu searching for Hubs itself: it talks to nothing but the agent (decision 048). Preselecting the first Hub: Return would send the code to whichever answered first.

**Links.** #250; decisions 045, 048, 049, 050, 055.
