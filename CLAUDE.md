# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in
this repository.

## Build and Test Commands

```bash
go build ./...           # Build
go run . help            # Run (defaults to "help" subcommand)
go test ./...            # Run all tests
go test -run TestName    # Run a single test
```

## Project Purpose

Transaction Agent fetches bank transactions from Plaid, writes OFX 2.2 files, and rewrites
those files so payee names match the ones already used in a GnuCash book. It runs on
Windows, macOS, and Linux.

It formerly used a prior aggregator's API, which was withdrawn in July 2026.

**Production is enabled.** Plaid's Trial plan allows ten Production Items for the lifetime
of the account, and removing one does not free its slot, so the guardrails live where the
cost is incurred: `plaid-link --env production` demands a typed confirmation and refuses
without a terminal.

**The production API secret is sealed behind a FIDO2 security key.** Sandbox is served
from the keyring; production is not. The secret is unsealed only by a key derived from a
security key's `hmac-secret` output, which costs a physical touch — the one act no process
running as the operator can perform, including this agent. Enroll with `plaid-enroll-key`
(two key slots, primary and backup); the sealed vault lives in the DB
(`plaid_wrapped_api_key`, migration 008), not the keyring, because it is ciphertext and the
secret is re-readable from the Dashboard. The lesser guards — TTY check, typed phrase,
`RefuseUnderAgent` on `CLAUDECODE`/`QWEN_CODE` — stop accidents but are all defeatable by
the process they guard; only the touch is the boundary.

**The cryptography is `github.com/jeffbstewart/touchvault`,** extracted from this
repository and now consumed as a dependency: the data key, the per-key wrapping, the
salt-dependence gate, the attestation policy, the sealed format, and the `webauthn.dll` FFI
(in its `fido` provider, which owns the `RefuseUnderTest`/`RefuseUnderAgent` guards). Read
its docs, not this file, for how any of that works, and fix bugs in it there — the
`securitykey` package and the crypto in `plaid/hardwarekey.go` are gone, not moved aside.

What stays here, in `plaid/hardwarekey.go`, is the glue that is genuinely Plaid's: one
vault per environment, the RP identity (`bankferry.invalid` — changing it orphans
every enrolled credential), the two-slot policy, and the routing that sends sandbox to the
keyring and production to the key. Two invariants of that glue are load-bearing:

- **The environment is part of the secret's name** (`plaid-<env>-api-key`). touchvault binds
  a secret's name into its AAD, so a sandbox vault moved into production's row cannot yield
  a production secret. This replaces an `if` statement with a decryption failure; do not
  reduce it back to one name.
- **The default trust anchors are the shipped policy.** `attestationRoots` is a var only so
  tests can enroll a synthetic authenticator; production trusts the bundled Yubico roots.
  Attestation cannot be switched off — touchvault always requires it — but do not widen the
  anchors without meaning to.

**The `uv` trap** (touchvault's concern, recorded here because it is the subtlest thing in
the design): Windows verifies the operator with a PIN whenever the key has one, even when
the code requests `uv=discouraged`, and a CTAP2 authenticator returns a *different*
`hmac-secret` value depending on whether it verified. Enrollment records what the
authenticator reported (never what was requested); every read requests the same; a mismatch
(e.g. a cleared PIN) surfaces as `touchvault.ErrUserVerificationMismatch`, not as corrupt
ciphertext.

### The link server is guarded four ways
`plaid-link` and `plaid-relink` run an HTTP server that holds a `link_token` and can
consume an irreplaceable Item. Anything that can route to it could otherwise drive Link
against its own bank and have the browser POST to our `/exchange`.

1. **Access key** — random per run, printed on startup, required on the entry page.
   Reaching the address is not enough.
2. **Session cookie** — minted by the entry page, required by every other handler. A
   request without it was never initiated here.
3. **OAuth window** — the callback is accepted only after the browser reports `OPEN_OAUTH`
   (`POST /oauth-arm`), and for `oauthWindow` (five minutes) afterwards. On expiry the
   server stops with `ErrOAuthTimeout` rather than waiting forever.
4. **Single use** — `session.claim()` lets the exchange complete exactly once, so a replay
   or a race cannot spend a second Item.

`/healthz` is the only unauthenticated endpoint. It echoes the `Host` header, sanitized,
served as `text/plain` with `nosniff`, so a reverse proxy can be verified before an Item is
spent.

The server binds loopback by default (`DefaultBindAddr`). `--bind` opts into a wider bind
and warns. `--redirect-uri` must be HTTPS unless it is loopback, is validated before any
Plaid call, and **must be registered in the Plaid Dashboard** — Plaid returns
`INVALID_FIELD` otherwise. Plaid never fetches the redirect URI; only the user's browser
does, so it need not face the internet.

The OAuth callback resumes Link with a `receivedRedirectUri` the **server reconstructs**
from the configured redirect URI, never one the caller supplies.

## Architecture Overview

```
main.go                        Entry point: delegates to cli.Run(os.Args)
  |
  cli/                         CLI dispatcher and subcommands
  |  cli.go                      Run() switches on command: help|learn|map
  |  learn.go                    runLearn() — parse GnuCash, extract payees into DB
  |  map.go                      runMap() — transform OFX files with learned payee names
  |  io.go                       stdout()/stderr() helpers
  |
  civildate/                   Timezone-free calendar date type
  |  civildate.go                ISO8601Date: New, FromTime, Today, Parse, String ("YYYY-MM-DD"),
  |                              Format(layout), Compare, IsZero, UnmarshalJSON
  |
  source/                      Provider-neutral account and transaction types
  |  source.go                   Account, Transaction, Institution, AccountType, AccountSubtype
  |
  gnucash/                     Read-only GnuCash XML parser
  |  gnucash.go                  File type, Parse(), ParseReader()
  |                              Streaming XML decoder; extracts distinct transaction descriptions
  |
  payee/                       Payee matching engine
  |  payee.go                    Payee/Rule/Match types, Matcher, NewMatcher(), Match()
  |                              Case-insensitive substring matching with priority ordering
  |
  ofx/                         OFX 2.2 XML document generation and parsing
  |  ofx.go                      Statement/Transaction/Balance types, Write(w, stmt), Read(r)
  |                              ofxDate() helper formats civildate as YYYYMMDD
  |
  ofxexport/                   Fetch→filter→write→mark orchestration
  |  ofxexport.go                TransactionFetcher/ExportStore interfaces, Exporter, ExportAccount()
  |                              buildStatement(), ofxTransactionType(), mapAccountSubtype()
  |                              Currently has no Fetcher implementation — awaiting a source adapter.
  |
  secrets/                     OS keyring credential storage
  |  secrets.go                  Store(), Load(), Delete()
  |                              Uses 99designs/keyring (Keychain/WinCred/SecretService)
  |
  db/                          SQLite database for export tracking and payee learning
  |  db.go                       Open(), IsExported(), MarkExported(), runMigrations()
  |                              Payee and rule CRUD
  |  embed.go                    Embeds migrations/*.sql
  |  migrations/                 001–002: export tracking, 003–005: payee learning,
  |                              006: drop account mapping
  |
  example.env                  Template for .env configuration
```

## Data Flow

```
GnuCash file ──gzip XML──► gnucash.Parse()
                                │
                                ▼
                          learn command
                            └── extract distinct descriptions → payee + payee_rule tables

OFX files ──ofx.Read()──► map command
                            ├── match: payee rules (case-insensitive substring)
                            ├── review: interactive console for unmapped
                            ├── transform: clean NAME
                            └── ofx.Write() → mapped/*.ofx
```

`ofxexport` implements the source→OFX half of the pipeline but has no live
`TransactionFetcher`.

## Key Conventions

### Error handling
Never ignore returned errors. Every error must be handled:
- In production code, return the error to the caller, or log it with `log.Printf` when on a
  cleanup/defer path (e.g. `defer resp.Body.Close()`, `defer conn.Close()`). When logging a
  cleanup error on a short-circuit return, still return the original error.
- In tests, check errors with `t.Fatalf` or `t.Errorf` — including `defer Close()` calls
  (wrap in a closure) and `fmt.Fprint` writes to `httptest` response writers.
- Never use bare `defer x.Close()` — always capture and handle the error.

### Date handling
All dates use `civildate.ISO8601Date` — a timezone-free year/month/day value. The
`civildate` package provides `New()`, `FromTime()`, `Today()`, `Parse()`, `String()` (ISO
8601 "YYYY-MM-DD"), `Format(layout)`, `Compare()`, `IsZero()`, and `UnmarshalJSON`. OFX's
"YYYYMMDD" format is handled by the `ofxDate()` helper in the `ofx` package.

### Provider-neutral types
`ofxexport` consumes `source.Account` and `source.Transaction`, never a provider's own
structs. A new data provider is added by writing an adapter that populates these types and
satisfies `ofxexport.TransactionFetcher`. Do not reintroduce provider types into
`ofxexport`, `db`, or `ofx`.

### FITID stability
`source.Transaction.ID` is written to OFX as `FITID`. **GnuCash deduplicates imports on
FITID alone, never on content.** An ID that changes between fetches of the same transaction
produces a duplicate in the user's book — this was observed with Teller when enrollments
were re-authorized. An adapter whose provider lacks stable identifiers must synthesize a
stable one from transaction content.

### No account mapping
The GnuCash account hierarchy and the payee→account mapping were removed (migration 006).
GnuCash discards the OFX `MEMO` field on import, so a predicted destination account never
reached the book. Do not reintroduce account classification during capture.

### Secret storage
`secrets` exposes generic `Store(key, data, label, description)`, `Load(key)`,
`Keys(prefix)`, and `Delete(key)` over the OS credential store via `99designs/keyring`,
under the service name `"bankferry"`.

**`github.com/danieljoos/wincred` must stay at v1.2.3 or later.** `keyring v1.2.2` depends
on `wincred v1.1.2`, which routes `CredReadW` through a Go interface (`type proc interface{
Call(...) }`, "helps testing"). `syscall.Proc.Call` is annotated `//go:uintptrescapes`, and
that annotation only applies to *direct* calls — going through an interface silently
defeats it. The `&pcred` argument is then neither kept alive nor pinned, the goroutine
stack can move during the syscall, and `CredReadW` writes the credential pointer to a stale
address. `wincred` returns `(nil, nil)` and `keyring` dereferences nil.

The symptom is a nil-pointer panic inside `secrets.Load` that depends on goroutine stack
depth: deterministic for a given test, absent under `go run`, absent once other tests have
grown the stack. `v1.2.3` calls `syscall.SyscallN(procCredRead.Addr(), ...)` directly. Do
not let a dependency resolution drag `wincred` back below v1.2.3.

### OFX output
One `.ofx` file per account, named `{SanitizedInstitution}_{LastFour}_{Timestamp}.ofx`. OFX
2.2 XML format. Bank accounts produce `BANKMSGSRSV1`; credit cards produce
`CREDITCARDMSGSRSV1`. Transaction amounts are signed decimal strings (e.g. `"-45.00"`);
`source` amounts are sign-flipped on the way into OFX.

**Nothing may ever be written over an `.ofx` file.** None of the three parts of that name
is unique — the timestamp is good only to one second — so two accounts sharing an
institution and a mask collide. Two logins at one bank is the case that makes it reachable.
Overwriting loses transactions *silently*: the replaced file's transactions are still
recorded as exported and the cursor still advances past them, and Plaid never re-delivers
them. Three things keep that shut, and all three are load-bearing:

- **The final name must be free before anything is written** (`Exporter.Exists`). A rename
  replaces its target on every OS this runs on, so checking at rename time is too late.
- **The statement is written to `{final}.part` and renamed into place.** The pending name is
  the final name plus a constant suffix and nothing else. That transform is injective, so
  two accounts collide on it exactly when they would collide on the final name — which is
  what makes the next point work. The suffix must not end in `.ofx`: `map` reads every
  `.ofx` file in the directory, and a half-written statement is not one.
- **The pending file is created with `O_EXCL`** (`cli.createExclusive`; never `os.Create`,
  which truncates). Within one batch no final name exists yet, so the free-name check passes
  for both colliding accounts; this is what catches them.

A collision is refused, not disambiguated. If that refusal ever fires in practice, add a
discriminator to the filename rather than weakening any of the above.

Console output identifies accounts by name *and* mask (`cli.accountLabel`), and Items by
institution *and* item ID (`cli.itemLabel`). Neither name identifies anything on its own:
Chase calls every credit card "CREDIT", and two logins at one bank are two Items with the
same institution name.

### Transaction type mapping
`ofxexport.ofxTransactionType()` derives OFX `TRNTYPE` from the sign of the source amount:
negative is `DEBIT`, otherwise `CREDIT`. Finer OFX types did not survive GnuCash import, and
providers vary in what metadata they expose. `ofxexport.mapAccountSubtype()` maps source
account subtypes to OFX ACCTTYPE (checking/savings/money_market, everything else defaults to
checking).

### Database
SQLite via `modernc.org/sqlite` (pure Go, no CGo). WAL journal mode, foreign keys enabled.
Migrations are embedded SQL files in `db/migrations/`, applied in lexicographic order,
tracked in `migrations_applied` table. The `exported_transaction` table uses `INSERT OR
IGNORE` for idempotent marking; its columns are still named `teller_*` because migration
002 has already been applied to live databases and renaming them requires a new migration
rather than an edit to the old one.

### The sync cursor must never advance alone
`plaid_sync_state` (migration 007) holds one `/transactions/sync` cursor per Plaid Item,
keyed by `(environment, item_id)` so a sandbox cursor can never be applied to production.

**Plaid never re-delivers a transaction once the cursor has moved past it.** A cursor that
advanced without its transactions being recorded loses them permanently. `db.CommitSync`
therefore writes the exported transaction IDs and advances the cursor in one database
transaction. Do not add a bare `SaveCursor`.

The opposite order is safe: if the process dies after writing OFX files but before
`CommitSync`, the cursor has not moved, the next run receives the same transactions, the
files are rewritten, and GnuCash deduplicates them on FITID. That only holds because the
FITID is stable.

An empty cursor is refused at both the Go and SQL layers, because storing one silently
replays the Item's entire history. `LoadSyncCursor` returns `(cursor, found, err)` so absent
is never confused with empty.

### What the security key costs, and what touchvault refuses
The gesture count is a design constraint, not a detail: a key that blinks more often than
the operator can explain teaches them to touch it without reading, which destroys the only
thing the touch is worth. The current tally:

| Act | Gestures | Notes |
|---|---|---|
| Read the production secret (`fetch`, `plaid-link`, …) | 1 | Once per command, at the composition root. Never lazily. |
| `plaid-enroll-key`, first key | 3 | Create, derive, and prove the derivation depends on the whole salt. |
| `plaid-enroll-key`, backup key | 1 + 3 | One on an enrolled key to recover the data key, three on the new one. The secret is never needed again. |
| `plaid-delete-key-slot --key-slot <n>` | 1 | On a key that is **still** enrolled, so a lost key can be retired. |
| `plaid-delete-key-slot --force` | 0 | Destroys the vault. Gesture-free on purpose: the reason to be here is that the key is gone, and a vault that demanded a touch to forget could never be forgotten. |
| `plaid-list-key-slots` | 0 | Slots are authenticated metadata; reading them unlocks nothing. |

touchvault enforces the things that keep that honest, and none of them are ours to relax:
enrollment refuses a key whose `hmac-secret` output does not provably depend on the whole
salt (`ErrDerivationIgnoresSalt`) and one that cannot attest to a trusted hardware root
(`ErrUntrustedAuthenticator`, `ErrNoAttestation`), so a software authenticator cannot hold
the secret; unlocking refuses a vault that never passed the salt gate
(`ErrNotEntropyVerified`) **before** spending a touch; and removing the last key is refused
(`ErrLastKey`) rather than silently stranding the secret. If a change here starts wanting
one of those to be optional, the change is wrong.

**Do not add a path that reaches hardware except through `fido.New`.** It is the single door,
and it refuses under a test binary and under a coding agent's shell. Everything in this
program takes a `touchvault.Authenticator`, so a fake reaches every function and a real key
reaches none of them by accident. That is why `go test ./...` never makes a key blink, and
it is worth more than any test it makes awkward.

### Testing
All packages use the standard `testing` package. Key test patterns:
- **secrets:** `mockKeyring` struct + `openKeyringFunc` var-swap
- **db:** `:memory:` SQLite and `t.TempDir()` for file tests
- **ofxexport:** `mockFetcher`, `mockStore`, `mockWriteCloser` with injectable errors
- **ofx:** round-trip tests (Write then Read) for bank, credit card, special characters
- **gnucash:** embedded gzip XML fixture, tests for gzip and plain XML parsing
- **payee:** priority ordering, case insensitivity, no-match, empty rules
- **plaid (the vault):** `plaid/vaultfake_test.go` holds a fake `touchvault.Authenticator`
  and a synthetic vendor PKI for it to attest to, plus `useTestRoots`, which points
  `attestationRoots` at that PKI for one test. A fake cannot chain to Yubico's roots, so
  without this nothing could be enrolled in a test at all. The test that leaves the anchors
  at their default and watches the synthetic key get *rejected* is the one that pins the
  shipped policy; keep it.

### CLI commands
`cli.Run()` dispatches on `os.Args[1]` (default: `"help"`). Run `help` for the full text; it
is grouped and explains what each command costs.

Plaid: `plaid-init`, `plaid-link`, `plaid-items`, `plaid-relink`, `plaid-reset-login`,
`plaid-remove`, `plaid-export`, `plaid-verify-backup`. Production security key:
`plaid-enroll-key`, `plaid-list-key-slots`, `plaid-delete-key-slot`. Fetching: `fetch`.
GnuCash: `learn`, `map`.

Every run calls `warnStaleBackups`, which nags when the keyring holds Items no export
covers.

### fetch: the ordering is the design
`fetch` syncs one Item at a time, converts through `plaid.SourceAccounts` /
`plaid.SourceTransactionsByAccount`, exports one OFX file per account, and only then calls
`db.CommitSync`.

**Files are written before the cursor advances.** Plaid never re-delivers a transaction
once its cursor passes it, so advancing first and crashing loses those transactions
forever. Crashing the other way is harmless: the next run receives the same transactions,
rewrites the files, and GnuCash deduplicates them on FITID. If `CommitSync` fails, the files
just written are removed.

The write is two-phase, in three steps per Item: every account's statement is written to its
`.part` file, then `cli.commitFiles` renames all of them into place, and only then does
`CommitSync` run. The cursor covers every account under an Item, so one account's failure
abandons the whole Item — every `.part` file goes, including those of the accounts that
succeeded, because committing a cursor over a partial set loses the missing account's
transactions. No filesystem renames a set atomically and this does not pretend to: a rename
is far likelier to succeed than the write before it, so the window where only some files are
visible is microseconds wide and nothing is recorded while it is open. A rename that does
fail undoes the ones before it.

`ofxexport` therefore does **not** record what it exported. It returns `ExportedIDs`, and the
caller commits them together with the cursor in one database transaction. Do not give
`ExportStore` a `Mark` method.

Investment and loan accounts are filtered out by the adapter. Pending transactions are
dropped by the exporter, and that is load-bearing: a pending transaction posts under a
*different* ID, so exporting it would import the purchase twice. `modified` and `removed`
transactions are reported, never silently applied — nothing here can un-import from GnuCash.

### Payee matching
The `payee` package implements case-insensitive substring matching with priority-based rule
ordering. Rules come from two sources: auto-extracted from GnuCash (priority 0) and
user-created during `map` review (priority 10). Higher priority rules match first; at the
same priority, longer patterns match first. Matched payee names go into the OFX NAME field.

## Environment Variables

Configuration is loaded from a `.env` file in the project root via `godotenv`. See
`example.env` for the four variables: `OFX_OUTPUT_DIR`, `DATABASE_PATH`, `DRY_RUN`,
`GNUCASH_FILE`.

**Never read, display, log, or load the contents of `.env` into any context. Never commit
`.env` to source control.**

## Version Control

This repository uses **Subversion (svn)**, not git. When creating new files, always add them
to svn tracking with `svn add <file>`. The `.env` file is svn-ignored and must never be
committed.

### Update the docs in the same commit

Behavior lives in two places: the code, and the prose that describes it. A change that alters
anything observable — a command, a flag, a default, an error message, an invariant, a security
property, a gesture count — is **not finished** until the docs that describe that behavior are
updated in the *same* commit. The recurring failure mode is a fixed bug or a renamed flag
whose documentation still asserts the old behavior; a doc that lies is worse than no doc,
because it is trusted.

Before committing, check each surface the change could have outdated:

- **README.md** — the `Status` list, the command examples, the `Configuration` table, and any
  "What this code knows" note the change touches.
- **CLAUDE.md** — the conventions and invariants above; this file is the map new work reads
  first, so a stale entry here misleads every future change.
- **`help` output** and other user-facing strings, when a command or its cost changed.
- Any design/history doc still present that describes what changed.

A quick grep for the old flag, name, default, or claim before you commit catches most of
these. When you cannot update a doc in the same commit, say so explicitly rather than letting
the mismatch land silently.
