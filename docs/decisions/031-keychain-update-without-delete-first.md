# 031 — Keychain updates never delete the previous value

- Context: Replacing a credential deleted its existing Keychain item before attempting the new write, so a transient write failure could remove the last valid value (#148).
- Decision: Encode before any Security call, update only the value of an existing item, and add only after Update confirms that the item is absent. A duplicate Add race gets one bounded Update retry; no write-failure path deletes a credential.
- Decision: A failed or corrupt read does not mutate stored bytes, and callers must not publish success or start persistence-dependent work after a write failure.
- Why: A visible, retryable failure preserves trust state; deleting the prior credential turns a temporary storage problem into credential loss and false registration state.
- Rejected alternative: Delete-then-Add, automatic credential reset, or treating write failure as success, because each can discard valid state or hide that dependent work lacks durable credentials.
- Links: issue [#148](https://github.com/psimaker/vaultsync/issues/148); `KeychainService.swift`; `KeychainServiceTests.swift`.
