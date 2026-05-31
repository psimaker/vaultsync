# App Store Connect — Cloud Relay pricing changes

These steps must be done manually in App Store Connect (no public API for product creation). The app code and `ios/VaultSync.storekit` already expect them.

## 1. Raise the monthly price

1. App Store Connect → **Apps → VaultSync → Monetization → Subscriptions**.
2. Open subscription group **Cloud Relay** → product **Cloud Relay** (`eu.vaultsync.app.relay.monthly`).
3. **Subscription Prices → +** → set the new base price (reference: **USD 1.99/month**, was 0.99).
4. When prompted, **preserve the current price for existing subscribers** (recommended — avoids churn/consent friction for the 17 existing subscribers); new subscribers pay the new price.

## 2. Add the yearly plan

1. Same subscription group **Cloud Relay** → **Create Subscription**.
2. **Reference Name:** `Cloud Relay Yearly`
3. **Product ID:** `eu.vaultsync.app.relay.yearly` (must match exactly — the app loads this ID).
4. **Subscription Duration:** 1 Year.
5. **Price:** reference **USD 14.99/year** (≈ 1.25/mo, ~37 % below monthly).
6. **Localizations:** display name e.g. "Cloud Relay (Yearly)"; description "Silent push wake-ups for faster server-to-iPhone sync. Best value, billed yearly."
7. **Ranking within the group:** place yearly and monthly on appropriate levels. Same feature set, so a crossgrade is fine; if forced to rank, put yearly as the higher tier so monthly → yearly is an immediate upgrade.

## 3. Review notes

- No free trial / introductory offer is configured (product decision 2026-05-31).
- The price shown in-app is always read from StoreKit at runtime (`priceText(for:)`), never hard-coded — both plans display the storefront-correct localized price automatically.
- Subscription disclosures (price/period, auto-renew until canceled, how to cancel, Terms/Privacy links) are shown in **Settings → Cloud Relay** and the in-context upsell, satisfying Guideline 3.1.2.
- Cloud Relay requires a one-time server-side helper. To stay within Guideline 3.1.2(a) ("get what they've paid for without performing additional tasks"), the helper is framed as part of using the product (the in-app "Set Up Your Server" step shown right after purchase), not as a gate to unlock paid content.
