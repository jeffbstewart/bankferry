# The investments-snapshot contract (bankferry → finance2)

`plaid_snapshot.proto` is the wire contract for the **investments
export**: bankferry fetches holdings and investment transactions from
Plaid and emits one `InvestmentsSnapshot` file per run; finance2
imports that file and never touches Plaid, its secrets, or the
keyring. Design and rulings:
`finance2/docs/design/plaid-investments-pipeline.md` (2026-07-17,
amended 2026-08-20).

**This copy is the primary source** (ruling, Jeff 2026-07-17: the
proto lives with the writer). finance2 carries a verbatim clone at
`finance2/proto/plaid_snapshot.proto`. The file was authored in
finance2 on 2026-08-20 to bootstrap the importer, which is already
built and merged; from here on, contract changes land **here first**,
and the same PR's description must note that finance2's clone needs
the same change. Bump `schema_version` on any breaking change — the
finance2 reader rejects versions it does not know (it currently
accepts exactly `1`).

## Status

The writer is **brokerferry**, a second binary built from this module under
its own Plaid credentials (see `CLAUDE.md`, "Two programs, one module"). Its
`investments` verb:

1. For each of brokerferry's Items, calls `/investments/holdings/get` and
   `/investments/transactions/get` through the raw-JSON decimal-safe client
   (`json.Number` end to end — the official SDK's float fields are forbidden
   near money or quantities, same as the transactions fetcher).
2. Builds one `InvestmentsSnapshot` (schema_version `1`, `as_of` = the fetch
   date) and writes the binary proto, and optionally a `.json` debug form,
   into `INVESTMENTS_OUTPUT_DIR`. That directory is git-ignored — snapshots
   hold real account names and values.
3. Requests the `investments` product at enrollment: brokerferry's Items are
   created with it, so no update-mode re-link is needed and bankferry's own
   link flow is untouched.

No push, no network to finance2: the human uploads the file through
finance2's Imports screen (browser session). Runs stay human-initiated
behind the existing guards.

## Codegen

The generated `proto/investments/plaid_snapshot.pb.go` is committed, so
building and testing need no protoc. Regenerate only when the `.proto`
changes, with protoc on PATH (finance2 compiles the same contract with
protoc 4.34.1) and `protoc-gen-go` at the version `go.mod` pins as a tool:

```bash
go build -o "$(go env GOPATH)/bin/protoc-gen-go" google.golang.org/protobuf/cmd/protoc-gen-go
go generate ./proto/
```

`proto/generate.go` carries the `//go:generate` line; the output must be
byte-identical to what is committed.

## Field mapping (Plaid API → proto)

| Proto field | Plaid source | Notes |
|---|---|---|
| `ItemSnapshot.institution_entry` | institution name for the Item | e.g. `"Vanguard"` |
| `ItemSnapshot.item_ref` | `item_id` | opaque; **never** the access token |
| `Account.account_ref` | `account_id` | opaque, stable — finance2 keys its account links on it |
| `Account.name` / `official_name` / `mask` | same-named account fields | mask is the human's matching aid in finance2's link UI |
| `Account.type` / `subtype` | same-named | e.g. `investment` / `401k` |
| `Account.institution_value` | `balances.current` | |
| `Account.cash_balance` | `balances.available`, when reported | finance2 uses it for the sweep; omit rather than guess |
| `Holding.security` | joined from the response's `securities[]` by `security_id` | |
| `Holding.quantity` | `quantity` | via `json.Number` → string, verbatim |
| `Holding.cost_basis` | `cost_basis` | omit when Plaid omits it |
| `Holding.institution_price` / `price_as_of` | `institution_price`, `institution_price_as_of` | |
| `Holding.institution_value` | `institution_value` | |
| `SecurityRef.*` | `securities[]` entry | any of ticker/cusip/isin may be empty; set `is_cash_equivalent` faithfully — finance2 folds those into cash |
| `InvestmentTransaction.*` | `/investments/transactions/get` rows | archived by finance2 but not yet processed; fetch window per run |

## What finance2 does with it (so the writer knows what matters)

- Uploads validate schema version and shape, then **archive the bytes
  verbatim**; processing is separate and freely repeatable.
- Plaid accounts are linked to finance2 accounts **by the human, by
  `account_ref`** — keep it stable across runs.
- Tax-deferred linked accounts: holdings quantities and the sweep are
  upserted with `plaid` provenance. **Securities match by ticker** —
  a holding with an empty or unknown ticker is flagged, not imported,
  so populate `ticker` whenever Plaid provides one.
- Taxable accounts: compared against hand-maintained lots and
  reported; never mutated.
- Holdings are levels, not deltas — each snapshot is full state per
  run; bankferry stays stateless beyond fetch bookkeeping.

## Hard rules

- **No float/double anywhere near money or quantities** — exact
  decimal strings end to end.
- **No secrets in the snapshot**: no access tokens, no request IDs
  tied to credentials.
- **Snapshot files are data, never repo content**, in either
  repository.
