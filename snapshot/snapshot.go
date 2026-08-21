// Package snapshot builds and writes the investments snapshot brokerferry
// hands to finance2: proto/plaid_snapshot.proto, one file per run.
//
// It is the only place Plaid's investments shapes become the contract's. The
// mapping is deliberately dumb — every value is copied, none is computed —
// because the contract promises finance2 the literal Plaid sent, and because
// holdings are levels, not deltas: each snapshot is the full state at fetch
// time, and brokerferry keeps no memory of the last one.
package snapshot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/plaid"
	pb "github.com/jeffbstewart/bankferry/proto/investments"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// SchemaVersion is the contract version this writer produces. finance2's
// reader accepts exactly this value; bump both together.
const SchemaVersion = 1

// ItemData is one Item's fetch: its identity and what the two investments
// calls returned for it.
type ItemData struct {
	Item         plaid.Item
	Holdings     plaid.HoldingsResult
	Transactions plaid.InvestmentTransactionsResult
}

// Build assembles one snapshot from every Item fetched this run.
//
// A holding or transaction that names a security absent from the response's
// securities table is an error, not an empty reference: finance2 matches
// holdings to its own securities by ticker, and a blank SecurityRef would be
// imported as "unknown security" rather than caught.
func Build(asOf civildate.ISO8601Date, items []ItemData) (*pb.InvestmentsSnapshot, error) {
	if asOf.IsZero() {
		return nil, errors.New("snapshot: as_of date is zero")
	}

	out := &pb.InvestmentsSnapshot{
		SchemaVersion: SchemaVersion,
		AsOf:          date(asOf),
	}
	for _, it := range items {
		item, err := buildItem(it)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func buildItem(it ItemData) (*pb.ItemSnapshot, error) {
	if it.Item.ItemID == "" {
		return nil, errors.New("snapshot: an item has no item ID")
	}

	// Both calls carry a securities table. The holdings one is the fuller
	// record, so it wins; the transactions table only adds securities that
	// appear in no current position (something sold during the window).
	securities := make(map[string]plaid.Security)
	for _, s := range it.Holdings.Securities {
		securities[s.ID] = s
	}
	for _, s := range it.Transactions.Securities {
		if _, ok := securities[s.ID]; !ok {
			securities[s.ID] = s
		}
	}

	holdingsByAccount := make(map[string][]*pb.Holding)
	for _, h := range it.Holdings.Holdings {
		sec, ok := securities[h.SecurityID]
		if !ok {
			return nil, fmt.Errorf("snapshot: item %s: holding in %s names unknown security %q",
				it.Item.ItemID, h.AccountID, h.SecurityID)
		}
		holdingsByAccount[h.AccountID] = append(holdingsByAccount[h.AccountID], &pb.Holding{
			Security:         securityRef(sec),
			Quantity:         &pb.Decimal{Value: h.Quantity},
			CostBasis:        money(h.CostBasis),
			InstitutionPrice: money(h.InstitutionPrice),
			PriceAsOf:        optionalDate(h.PriceAsOf),
			InstitutionValue: money(h.InstitutionValue),
		})
	}

	txnsByAccount := make(map[string][]*pb.InvestmentTransaction)
	for _, t := range it.Transactions.Transactions {
		var ref *pb.SecurityRef
		if t.SecurityID != "" {
			sec, ok := securities[t.SecurityID]
			if !ok {
				return nil, fmt.Errorf("snapshot: item %s: transaction %s names unknown security %q",
					it.Item.ItemID, t.ID, t.SecurityID)
			}
			ref = securityRef(sec)
		}
		amount := t.Amount
		txnsByAccount[t.AccountID] = append(txnsByAccount[t.AccountID], &pb.InvestmentTransaction{
			TransactionRef: t.ID,
			Date:           date(t.Date),
			Name:           t.Name,
			Type:           t.Type,
			Subtype:        t.Subtype,
			Security:       ref,
			Quantity:       &pb.Decimal{Value: t.Quantity},
			Amount:         money(&amount),
			Price:          money(t.Price),
			Fees:           money(t.Fees),
		})
	}

	item := &pb.ItemSnapshot{
		InstitutionEntry: it.Item.InstitutionName,
		ItemRef:          it.Item.ItemID,
	}
	known := make(map[string]bool)
	for _, a := range it.Holdings.Accounts {
		known[a.AccountID] = true
		item.Accounts = append(item.Accounts, &pb.Account{
			AccountRef:       a.AccountID,
			Name:             a.Name,
			OfficialName:     a.OfficialName,
			Mask:             a.Mask,
			Type:             a.Type,
			Subtype:          a.Subtype,
			InstitutionValue: money(a.Current),
			CashBalance:      money(a.Available),
			Holdings:         holdingsByAccount[a.AccountID],
			Transactions:     txnsByAccount[a.AccountID],
		})
	}

	// Data for an account the holdings call did not list has nowhere to go
	// and must not vanish silently.
	for accountID := range holdingsByAccount {
		if !known[accountID] {
			return nil, fmt.Errorf("snapshot: item %s: holdings for unlisted account %q", it.Item.ItemID, accountID)
		}
	}
	for accountID := range txnsByAccount {
		if !known[accountID] {
			return nil, fmt.Errorf("snapshot: item %s: transactions for unlisted account %q", it.Item.ItemID, accountID)
		}
	}
	return item, nil
}

func securityRef(s plaid.Security) *pb.SecurityRef {
	return &pb.SecurityRef{
		PlaidSecurityId:  s.ID,
		Ticker:           s.Ticker,
		Cusip:            s.CUSIP,
		Isin:             s.ISIN,
		Name:             s.Name,
		Type:             s.Type,
		CurrencyCode:     s.Currency,
		IsCashEquivalent: s.IsCashEquivalent,
	}
}

// money maps an optional amount; absent stays absent.
func money(a *plaid.DecimalAmount) *pb.Money {
	if a == nil {
		return nil
	}
	return &pb.Money{Amount: &pb.Decimal{Value: a.Value}, CurrencyCode: a.Currency}
}

func date(d civildate.ISO8601Date) *pb.Date {
	return &pb.Date{Year: int32(d.Year()), Month: int32(d.Month()), Day: int32(d.Day())}
}

func optionalDate(d civildate.ISO8601Date) *pb.Date {
	if d.IsZero() {
		return nil
	}
	return date(d)
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

// PendingSuffix is appended to a file's final name while it is being
// written. It must not end in a suffix any reader looks for.
const PendingSuffix = ".part"

// Writer puts a snapshot on disk under the same discipline as the OFX
// exporter: the final name must be free before anything is written, the
// bytes go to a pending file created exclusively, and the rename happens
// last. Nothing is ever written over an existing snapshot.
type Writer struct {
	// Dir receives the files. Created if absent.
	Dir string

	// JSON also writes a protojson rendering beside the binary file, for
	// reading by eye. It is a debug aid, not the contract.
	JSON bool

	// CreateFile opens a new file for writing and fails if it exists.
	CreateFile func(path string) (io.WriteCloser, error)

	// Exists reports whether a path is occupied.
	Exists func(path string) (bool, error)
}

// Result names what Write produced.
type Result struct {
	// Binary is the snapshot file finance2 imports.
	Binary string

	// JSON is the debug rendering, empty unless requested.
	JSON string
}

// Write serializes the snapshot into Dir as investments_{date}_{time}.pb
// and returns the paths written. On any failure nothing is left behind.
func (w Writer) Write(s *pb.InvestmentsSnapshot, now time.Time) (Result, error) {
	if w.Dir == "" {
		return Result{}, errors.New("snapshot: no output directory")
	}
	if w.CreateFile == nil || w.Exists == nil {
		return Result{}, errors.New("snapshot: writer is missing CreateFile or Exists")
	}
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("snapshot: creating %s: %w", w.Dir, err)
	}

	stem := filepath.Join(w.Dir, "investments_"+now.Format("20060102_150405"))
	binary := stem + ".pb"
	var jsonPath string
	if w.JSON {
		jsonPath = stem + ".json"
	}

	for _, p := range []string{binary, jsonPath} {
		if p == "" {
			continue
		}
		occupied, err := w.Exists(p)
		if err != nil {
			return Result{}, fmt.Errorf("snapshot: checking %s: %w", p, err)
		}
		if occupied {
			return Result{}, fmt.Errorf("snapshot: %s already exists; refusing to overwrite it", p)
		}
	}

	raw, err := proto.Marshal(s)
	if err != nil {
		return Result{}, fmt.Errorf("snapshot: encoding: %w", err)
	}

	var pending []string
	cleanup := func() {
		for _, p := range pending {
			if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				fmt.Fprintf(os.Stderr, "snapshot: remove %s after a failed write: %v\n", p, rerr)
			}
		}
	}

	if err := w.writePending(binary+PendingSuffix, raw); err != nil {
		cleanup()
		return Result{}, err
	}
	pending = append(pending, binary+PendingSuffix)

	if jsonPath != "" {
		text, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(s)
		if err != nil {
			cleanup()
			return Result{}, fmt.Errorf("snapshot: encoding JSON: %w", err)
		}
		if err := w.writePending(jsonPath+PendingSuffix, text); err != nil {
			cleanup()
			return Result{}, err
		}
		pending = append(pending, jsonPath+PendingSuffix)
	}

	// Rename last. A rename that fails undoes the ones before it.
	var renamed []string
	for _, p := range pending {
		final := p[:len(p)-len(PendingSuffix)]
		if err := os.Rename(p, final); err != nil {
			for _, r := range renamed {
				if rerr := os.Remove(r); rerr != nil {
					fmt.Fprintf(os.Stderr, "snapshot: remove %s after a failed rename: %v\n", r, rerr)
				}
			}
			cleanup()
			return Result{}, fmt.Errorf("snapshot: renaming %s into place: %w", p, err)
		}
		renamed = append(renamed, final)
	}

	return Result{Binary: binary, JSON: jsonPath}, nil
}

func (w Writer) writePending(path string, data []byte) (err error) {
	f, err := w.CreateFile(path)
	if err != nil {
		return fmt.Errorf("snapshot: creating %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("snapshot: closing %s: %w", path, cerr)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("snapshot: writing %s: %w", path, err)
	}
	return nil
}
