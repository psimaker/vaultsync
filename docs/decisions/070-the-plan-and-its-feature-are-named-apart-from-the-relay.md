# 070 — The plan and its feature are named apart from the relay

**Context.** The app's subscription was called "Cloud Relay" wherever it appeared: on the tab, on the page that sells it, on the cards that offer it and in the status that says it works. The same name stood in diagnostics and in the relay service's errors, where it names a piece of infrastructure. Every run in the background, whatever started it, was titled "Background Sync …". A person without the subscription saw that title in the activity history, so it could not also name what the subscription adds.

**Decision.** There are three names, each kept for one meaning:

- **"VaultSync Plus"** names the plan wherever something is offered, bought, restored or approved:
  - the tab, now "Plus", and the page title;
  - the card after the first sync, the card on the home screen and the onboarding hint;
  - the restore and Ask to Buy texts, and the notice that no subscription is active.
- **"Background Sync"** (de „Hintergrund-Sync“) names the feature the plan adds: the server wakes this iPhone, and VaultSync syncs once iOS delivers the wake-up. It stands wherever the feature's state is shown or set up: "Background Sync active", "went quiet", the server-helper setup.
- **"Cloud Relay" and "Relay"** stay where diagnostics and the relay service's own errors describe the mechanism: Relay Diagnostics, "Relay Unreachable", provisioning errors and the push-token error.

Every run in the background is a **"Background Run …"** (de „Hintergrundlauf …“), for the runs iOS starts on its own and for those a wake-up starts alike. No text a person without the plan sees uses the feature's name for something they already have.

`PlanNameOnPurchaseSurfaceTests` holds the rule:
- an exact list of the English keys still allowed to say "Cloud Relay";
- no "Relay tab", and the tab named "Plus" in every language;
- every run titled "Background Run";
- the plan's name the same in every language.

Product IDs, StoreKit logic, the relay's API and the stored state keep their names.

**Why.** A name that means the plan in one place and the mechanism in another makes a diagnostics screen read like an upsell and an upsell read like infrastructure. A run title that shares the feature's name tells a person without the plan either that they already have it or that syncing in the background is paid; neither is true, and the second contradicts the promise that sync is free.

**Rejected alternatives.**
- **Renaming every "Relay" text, diagnostics included.** The diagnostics talk about a service people configure (`relay.vaultsync.eu`, `vaultsync-notify --doctor`), and the docs and the helper call it the relay.
- **Titling the runs by what started them** ("Background Sync" for a wake-up, another word for iOS's own runs). It needs the trigger in the stored outcome and a second set of texts for one list. One neutral word covers both.
- **"Background Refresh" for the runs.** It names iOS's own setting, and the same title also stands over runs a wake-up starts.

**Links.** Decision 065 (the plan picker's contrast); `ios/VaultSyncTests/PlanNameOnPurchaseSurfaceTests.swift`.
