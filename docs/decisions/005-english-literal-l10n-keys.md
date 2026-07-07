# 005 — The English literal string is the localization key

**Context:** Every user-facing string is localized to en/de/es/zh-Hans through `L10n.tr(...)`/`L10n.fmt(...)`; the key scheme decides what users see when a translation is missing and how findable strings are in code.

**Decision:** The key is the literal English string, present identically in all four `*.lproj/Localizable.strings` files; `en.lproj` maps it to itself.

**Why:** A missing or forgotten key falls back to readable English instead of leaking a raw identifier into the UI; any string visible on screen is greppable verbatim in the codebase; there is no separate key registry to keep in sync.

**Rejected alternative:** Symbolic keys (`sync.error.title` style) — a missed entry ships the identifier to users, and the extra naming layer buys nothing at this project's size.

**Links:** `ios/VaultSync/Resources/L10n.swift`, `ios/VaultSync/*.lproj/Localizable.strings`.
