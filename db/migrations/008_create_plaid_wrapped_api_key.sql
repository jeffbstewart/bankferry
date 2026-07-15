-- The production API secret, wrapped so that unwrapping it needs a touch on
-- a FIDO2 security key. One row per environment.
--
-- This is not in the OS keyring, and deliberately so. The keyring is for
-- irreplaceable secrets -- the Plaid access tokens, which no endpoint will
-- reissue. This blob is neither: it is ciphertext, inert without a physical
-- touch, and the API secret it protects can be re-read from the Plaid
-- Dashboard at any time. Lose it and you re-enroll in two minutes. So it
-- lives with the other replaceable, machine-local Plaid state -- the sync
-- cursors -- rather than pretending to be a secret at rest.
--
-- blob is the self-describing wrapped-key structure (see plaid.wrappedAPIKey):
-- its own format marker, salt, observed user-verification flag, the AES-GCM
-- ciphertext of the secret, and one wrapped copy of the data key per enrolled
-- key. Every field is authenticated, so tampering fails closed. Nothing in it
-- is a plaintext secret, which is the entire reason it can sit here.
--
-- The environment is the key. A sandbox secret is never hardware-wrapped --
-- sandbox is free and served straight from the keyring -- so in practice this
-- table holds only production, but the schema does not assume it.
CREATE TABLE plaid_wrapped_api_key (
    environment TEXT NOT NULL PRIMARY KEY,
    blob        BLOB NOT NULL CHECK (length(blob) > 0),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
