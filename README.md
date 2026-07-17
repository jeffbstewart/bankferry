# bankferry

Pulls bank transactions from [Plaid](https://plaid.com), writes OFX 2.2 files, and rewrites
the payee names to match the ones already used in your GnuCash book.

It exists to remove about an hour of weekly toil: downloading statements, importing them,
and correcting the same payee names by hand every time. Pure Go, no cgo. Runs on Windows,
macOS and Linux. Every credential lives in the operating system's credential store.

```
Plaid /transactions/sync ──► one OFX file per account ──► GnuCash import
                                       ▲
GnuCash book ──► learn ──► payee rules ┘
```

It formerly used a prior aggregator's API, which was withdrawn in July 2026.

## Status

Sandbox works end to end. Production is reachable only through a security key.

- **The production API secret is sealed behind a FIDO2 security key.** It cannot be read
  without a physical touch — not by an attacker with the disk, not by an AI agent running
  as the operator, not by the tool run by mistake. Enroll with `plaid-enroll-key`. See
  [Security model](#security-model).
- The security-key cryptography now lives in
  [`github.com/jeffbstewart/touchvault`](https://github.com/jeffbstewart/touchvault),
  extracted from this repository and consumed as a dependency.
- Chase, the account this was written for, is not yet tested with Plaid. A prior
  aggregator never returned usable data for it, but that is not a Plaid finding — the
  question is still open.

This is a personal tool, published because the *notes* may be worth more than the code.
Most of what is written down below cost real debugging, and none of it was easy to find.

## Build

```sh
go build ./...
go test ./...
go run . help
```

`bankferry help` explains every command and what each one costs.

## Configuration

```sh
cp example.env .env
```

| Variable | Required | Default | Description |
|---|---|---|---|
| `OFX_OUTPUT_DIR` | Yes | — | Directory holding `.ofx` files; `map` writes to `mapped/` beneath it |
| `DATABASE_PATH` | No | `bankferry.db` | Path to the SQLite database |
| `GNUCASH_FILE` | Yes (for `learn`) | — | Path to your GnuCash file |
| `DRY_RUN` | No | `true` | `fetch` reports what it would write, and writes nothing |

`.env` must **never** be committed. It is version-control-ignored.

## Use

```sh
bankferry plaid-init   --env sandbox                    # store API credentials
bankferry plaid-link   --env sandbox                    # link a bank, in a browser
bankferry plaid-export --env sandbox --out backup.tapb  # back up the access tokens
bankferry fetch        --env sandbox                    # sync → OFX files → cursor

bankferry learn                                         # read payees from GnuCash
bankferry map                                           # rewrite OFX payee names
```

### learn

Reads the GnuCash file — never writes to it — and extracts every distinct transaction
description into SQLite as a payee.

Each description becomes a case-insensitive substring rule at priority 0. Rules you create
by hand during `map` review land at priority 10, so they win. At equal priority, the longer
pattern wins. Re-run whenever the book has grown meaningfully; it is idempotent.

### map

Reads every `.ofx` in `OFX_OUTPUT_DIR` and, for each transaction, matches the raw
description against the learned rules, prompts you for anything unmatched and remembers the
answer, then rewrites the OFX `NAME` field. Transformed files land in
`OFX_OUTPUT_DIR/mapped/`. The originals are untouched.

### Importing into GnuCash

1. Run `map`.
2. **File → Import → Import OFX/QFX…**, and pick a file from `mapped/`.
3. Review the Generic Import Transaction Matcher dialog and click OK.

Assign destination accounts in GnuCash's import dialog, which already learns them with its
own Bayesian matcher. An earlier version predicted the account and wrote it into the OFX
`MEMO` field — GnuCash discards `MEMO` on import, so the prediction never reached the book.
Migration `006` drops it. Do not reintroduce account classification during capture.

## What this code knows that the documentation doesn't

### GnuCash deduplicates OFX imports on FITID alone, never on content

It decides whether it has seen a transaction by comparing `FITID` — not dates, amounts, or
descriptions. So an identifier that changes between fetches of the same transaction imports
a second time and leaves a duplicate in your book. Observed with a prior aggregator, whose
transaction IDs shifted whenever an enrollment was re-authorized.

Any fetch adapter must produce a `FITID` stable across fetches, synthesizing one from
transaction content if the provider's own identifier is not dependable.

### The sync cursor must never advance alone

Plaid never re-delivers a transaction once `/transactions/sync` moves past it. A cursor that
advances without its transactions being recorded loses them permanently. So `db.CommitSync`
writes the exported IDs and advances the cursor in one database transaction, and `fetch`
writes files *before* the cursor moves.

The opposite order is safe: crash after writing files and the next run receives the same
transactions, rewrites them, and GnuCash deduplicates on FITID. That only holds because the
FITID is stable.

The same reasoning bans overwriting an `.ofx` file. A filename carries the institution, the
account mask, and a timestamp good to one second, and two accounts can share all three — two
logins at one bank, most plausibly. Overwriting one would be silent: the replaced file's
transactions are still recorded as exported and the cursor still advances past them. So each
account's statement is written to a `.part` file whose name is the final name plus a
constant suffix, every one of them is renamed into place together, and only then does the
cursor move. The final name must be free before the write, and the `.part` file is created
exclusively — which catches two accounts that would collide, because deriving the pending
name from the final one means they collide there first. A collision is refused, never
resolved by guessing.

### An Item is one login, not one institution

`plaid-link` refuses by default to link an institution that already has an Item, before the
exchange — it is the exchange that creates the duplicate, and a duplicate spends one of the
ten forever. Re-linking a login you already have recovers nothing.

Two logins at one bank are a different thing: legitimately two Items, and worth two slots.
`--duplicate-of <item id>` names the existing Item the new one sits beside and permits that
one duplicate. Naming an Item rather than flipping an "allow duplicates" switch keeps the
guard alive for every other bank — pick a different already-linked institution in the browser
by mistake and it still refuses. The flag resolves before the security key is touched, so a
mistyped item ID costs a re-run rather than a gesture.

The limit is worth stating plainly: **nothing can tell a new login from a re-link of one you
already have.** A login's identity is not known until after the exchange, and the exchange is
what spends the slot. (Plaid's own duplicate detection compares account masks — post-exchange,
so it cannot help here.) `--duplicate-of` proves only that you know the institution is already
linked; with two Items at one bank, naming either permits a third. The real guard is your own
intent, declared before the browser opens.

### Plaid access tokens cannot be recovered

No endpoint returns an access token given an `item_id`. Lose the token and the Item can
never be removed and never be used — only abandoned. Re-linking creates a *duplicate* Item,
and a Trial account is allowed ten for the lifetime of the account. Removing one does not
return its slot.

Hence `plaid-export`, `plaid-verify-backup`, and a nag on every run while the keyring holds
Items no backup covers.

### Money is not a float

Plaid sends JSON *numbers* (`89.4`, `23631.9805`), and the official Go SDK decodes them into
`float64`. `2.675` becomes `2.67`. This project decodes with `json.Number` into a
fixed-point `money.Amount`.

### There is no single OFX sign convention

A bank `TRNAMT` is negative for money leaving the account; a credit-card `TRNAMT` is
positive for a charge. Plaid's convention is the opposite of both — money leaving is
positive. The sign flip lives in the OFX writer, keyed on statement type, and nowhere else.

### `wincred` below v1.2.3 corrupts memory

`keyring v1.2.2` pulls `wincred v1.1.2`, which routes `CredReadW` through a Go interface
"to help testing". `syscall.Proc.Call` is annotated `//go:uintptrescapes`, and that
annotation applies only to *direct* calls. Through an interface the argument is neither kept
alive nor pinned, the goroutine stack can move mid-syscall, and `CredReadW` writes the
credential pointer to a stale address.

The symptom is a nil-pointer panic whose presence depends on goroutine stack depth:
deterministic in one test, absent under `go run`, gone once other tests have grown the
stack. Do not let dependency resolution drag it below v1.2.3.

### Windows overrides your user-verification request

A FIDO2 `hmac-secret` assertion requested with `uv: discouraged` is verified with a PIN
anyway if the key has one. This matters because a CTAP2 authenticator holds *two*
independent per-credential secrets — `CredRandomWithUV` and `CredRandomWithoutUV` — and
returns a different value for the same salt depending on which occurred.

Record what the authenticator *reported* in its signed flags, never what you asked for.
Otherwise a Windows update that begins honoring the request silently locks you out, and the
failure presents as corrupt ciphertext.

## Security model

### The link server is guarded four ways

`plaid-link` runs a short-lived HTTP server holding a `link_token` that can consume an
irreplaceable Item. Anything able to route to it could otherwise drive Link against its own
bank and have the browser POST to `/exchange`.

1. **Access key** — random per run, printed at startup, required by the entry page.
2. **Session cookie** — minted by that page, required by every other handler.
3. **OAuth window** — the callback is accepted only after the browser reports `OPEN_OAUTH`,
   and only for five minutes.
4. **Single use** — the exchange can complete exactly once.

It binds loopback unless told otherwise. The OAuth callback resumes Link with a
`receivedRedirectUri` the *server* reconstructs, never one the caller supplies. `/healthz`
is the only unauthenticated endpoint. Plaid never fetches the redirect URI — only your
browser does — so it need not face the internet.

### Production requires a human, cryptographically

Every Plaid API secret is obtained through one interface:

```go
type APIKeyDecrypter interface {
    DecryptAPIKey(env Environment) (string, error)
}
```

Sandbox is served from the keyring without ceremony: it is free, and its Items are
replaceable. Production is served only after a FIDO2 security key derives the key that
unwraps the secret — which requires a physical touch.

That distinction is the whole point. A TTY check, a typed confirmation, and an
environment-variable check are all defeatable by any process running as the operator —
including an AI coding agent editing this repository, which is how much of this code was
written. **None of them are the boundary. The touch is the boundary**, because no process
can perform it.

The lesser guards remain, because they stop accidents before those reach the boundary.
`RefuseUnderTest` keeps `go test ./...` from making the key blink, which would train the
operator to touch it without reading the prompt. `RefuseUnderAgent` refuses when
`CLAUDECODE`, `QWEN_CODE` or similar markers are present. Neither is the boundary; both can
be defeated by the same process they guard against. Only the touch cannot.

And a touch proves a human was *present*, not that they understood what they approved — the
OS prompt is generic. So irreversible operations additionally require typing a phrase that
names the act.

#### How the sealing works

The cryptography is [`touchvault`](https://github.com/jeffbstewart/touchvault), a library
extracted from this project. A random 256-bit data key encrypts the secret once. Each
enrolled key wraps a copy of that data key under a key derived from its `hmac-secret`
output:

```
secret   --AES-256-GCM(dataKey)-->   ciphertext
dataKey  --AES-256-GCM(kek_i)---->   wrapped[i]        for each enrolled key i
kek_i    = HKDF-SHA256(prf_i, salt)
prf_i    = hmac-secret(credential_i, salt)             <- requires a touch
```

Two key slots, a primary and a backup, so losing one key is not a lockout. Enrolling the
backup never needs the secret again — it recovers the data key from a key already enrolled.
Every stored field is authenticated (AES-GCM AAD), so tampering fails closed. Only genuine
hardware can be enrolled: the key must attest to a trusted vendor root, so a software
authenticator — which could be copied — is refused. The sealed vault is ciphertext, inert
without a touch, and the secret it protects is re-readable from the Plaid Dashboard — so it
lives in the database beside the sync cursors, not in the keyring, which is reserved for the
irreplaceable access tokens.

```sh
bankferry plaid-init        --env production   # store the client ID (not the secret)
bankferry plaid-enroll-key  --env production   # prompt for the secret, seal it
bankferry plaid-enroll-key  --env production   # again: add a backup key
bankferry plaid-list-key-slots   --env production
bankferry fetch             --env production   # one touch to read
```

The slot is not yours to choose: the first key enrolled is the primary, the second is the
backup, and a third is refused. `plaid-delete-key-slot --key-slot <n>` retires one, and
costs a touch on a key that is *still* enrolled — so a key you have lost can still be
removed with the one you kept. Removing the last key is refused, because it would need a
touch from the key you are giving up; `--force` destroys the vault outright instead, with no
touch at all, which is what you want when every key is gone.

One caveat worth knowing: if the key has a PIN set, Windows verifies the operator with it
even when the code asks not to, and the derived secret then depends on that. Enrollment
records what the authenticator *did*, and every later read requests the same — so clearing
the key's PIN afterward changes the derived secret and is reported as a mismatch, not as
corrupt ciphertext.

#### What the touch does not protect, and future hardening

The touch gates *decryption*, not *use*. Once the secret is unwrapped it is plaintext in the
process for the rest of the run. So the design defends well against a lost laptop, an offline
attack on the stored blob (AES-256-GCM), and any process that never obtains a touch — but it
cannot defend against malicious code *in this repository*, because that code runs after the
touch and can copy or use the secret. Against that threat the key downgrades an offline
attack to an online one (the attacker must ride a single operator-initiated touched run); it
does not eliminate it. The honest backstop there is reviewing the diff, which is imperfect.

Inherent limits, not fixable by more hardware:

- **Ride the expected touch.** Compromised code can copy the secret during the weekly `fetch`
  touch you meant to give — no unexpected prompt appears.
- **The enroll input path.** `plaid-enroll-key` reads the secret in the clear from the
  prompt, before any wrapping; a compromised input path captures it with no touch at all.
- **Persistence.** A single touched run can re-wrap the secret under an attacker-controlled
  (software) authenticator, giving silent access afterward with no physical key and no
  plaintext ever written to disk.
- **The Dashboard.** The secret is re-readable from Plaid's web console, behind the
  operator's web auth, entirely outside this machinery.

Improvements worth making, in rough priority:

1. **Narrow the enroll window.** Nothing structural stops the code that reads the secret from
   keeping it, but running enrollment from a freshly reviewed, clean checkout shrinks the
   opportunity.

**Attestation on enrollment**, formerly the top of this list, is now enforced: enrollment
refuses any credential that cannot chain to a trusted hardware root, which closes the
virtual-authenticator enrollment and persistence vectors.

The security-key path previously ran through a v0.1.0 third-party wrapper on the most
sensitive code path; it now calls `webauthn.dll` directly, through the `fido` provider in
`touchvault` — which is where the only `unsafe` in the dependency tree lives, and where
enrollment, derivation, and the agent/test refusals now happen.

The value at risk is deliberately low — losing this secret costs a new email address and a
fresh Plaid account — so these are noted, not urgent.

## Layout

| package | role |
|---|---|
| `cli` | command dispatch; the composition root |
| `plaid` | API clients, Item storage, link server, backup, adapter, the production vault glue |
| `source` | provider-neutral account and transaction types |
| `money` | exact fixed-point currency |
| `ofx`, `ofxexport` | OFX 2.2 documents; fetch → filter → write |
| `gnucash`, `payee` | read the book; match payee names |
| `db` | SQLite: sync cursors, export tracking, payee rules |
| `secrets` | OS keyring |
| `civildate` | timezone-free calendar dates |

The database is created automatically. Migrations are embedded SQL in `db/migrations/`,
applied in lexicographic order and tracked in `migrations_applied`.

A new data provider is added by writing an adapter that populates `source.Account` and
`source.Transaction`. Provider types never enter `ofxexport`, `db`, or `ofx`.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
