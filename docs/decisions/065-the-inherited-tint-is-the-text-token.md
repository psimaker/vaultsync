# 065 — The inherited tint is the text token; fills name the brand teal

**Context.** The app set its root tint, and the `AccentColor` asset, to `vaultAccent`, the brand teal. Whatever inherits the tint draws it as text: toolbar buttons (Done, Cancel), the back button on iOS 18, the selected tab's label, links, menu glyphs, alert buttons. As text the brand teal measures 4.4:1 on white, short of WCAG AA; decision 044 already moved every text in the kit to `vaultAccentText`, but the inherited tint still handed the brand teal to everything not styled by hand.

**Decision.** The root tint in `VaultSyncApp` is `vaultAccentText`, and the `AccentColor` asset carries the same light, dark and Increase Contrast values, for system UI the `.tint` modifier does not reach. A control whose tint fills a shape — a toggle — names `.tint(Color.vaultAccent)` where it stands, so brand fills keep the brand teal. The kit's primary button keeps `vaultAccentProminent` with `vaultOnAccent`. Only light mode changes: the two tokens are the same color in dark.

**Why.** One central default reaches every inherited text, including screens written later; per-button exceptions for text miss the next one. A toggle that inherits the text token would only turn a shade deeper, but the brand fill is a choice made where the control stands.

**Rejected alternative.** Keep `vaultAccent` as the tint and set `vaultAccentText` on each text action (the state before): every new toolbar button, link and system control starts at 4.4:1 until someone notices. Making the brand teal itself darker: changes every fill, the icon and the brand for a problem only text has.

**Links.** #208; decision 044; `ios/VaultSync/App/VaultSyncApp.swift`, `ios/VaultSync/Resources/Theme.swift`, `Issue208DefaultTintTests`.
