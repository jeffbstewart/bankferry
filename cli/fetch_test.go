package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/money"
	"github.com/jeffbstewart/bankferry/ofx"
	"github.com/jeffbstewart/bankferry/ofxexport"
	"github.com/jeffbstewart/bankferry/plaid"
	"github.com/jeffbstewart/bankferry/source"
)

func txnOn(t time.Time) plaid.Transaction {
	return plaid.Transaction{Date: civildate.FromTime(t)}
}

func TestWithinDays(t *testing.T) {
	now := time.Now()
	txns := []plaid.Transaction{
		txnOn(now),                     // today: kept
		txnOn(now.AddDate(0, 0, -5)),   // 5 days ago: kept for days>=5
		txnOn(now.AddDate(0, 0, -9)),   // 9 days: kept for days>=9
		txnOn(now.AddDate(0, 0, -10)),  // 10 days: kept for days>=10 (boundary)
		txnOn(now.AddDate(0, 0, -30)),  // 30 days: dropped for days<30
		txnOn(now.AddDate(0, 0, -365)), // a year: usually dropped
	}

	kept, dropped := withinDays(txns, 10)
	if len(kept) != 4 || dropped != 2 {
		t.Fatalf("days=10: kept %d, dropped %d; want 4, 2", len(kept), dropped)
	}

	// The window is inclusive of its boundary day.
	kept, dropped = withinDays(txns, 9)
	if len(kept) != 3 || dropped != 3 {
		t.Errorf("days=9: kept %d, dropped %d; want 3, 3", len(kept), dropped)
	}

	// A wide window keeps everything.
	kept, dropped = withinDays(txns, 3650)
	if len(kept) != len(txns) || dropped != 0 {
		t.Errorf("days=3650: kept %d, dropped %d; want %d, 0", len(kept), dropped, len(txns))
	}
}

// The caller guards days>0 before calling, but the helper should still behave
// sanely at the boundary: days=0 means the cutoff is today, so only today's
// transactions survive.
func TestWithinDays_Zero(t *testing.T) {
	now := time.Now()
	txns := []plaid.Transaction{txnOn(now), txnOn(now.AddDate(0, 0, -1))}

	kept, dropped := withinDays(txns, 0)
	if len(kept) != 1 || dropped != 1 {
		t.Errorf("days=0: kept %d, dropped %d; want 1, 1", len(kept), dropped)
	}
}

// ---------------------------------------------------------------------------
// Account labels
// ---------------------------------------------------------------------------

func TestAccountLabel(t *testing.T) {
	cases := []struct{ name, mask, want string }{
		// Chase names every credit card "CREDIT", so two cards on one login
		// are told apart only by the mask.
		{"CREDIT", "1234", "CREDIT (*1234)"},
		{"CREDIT", "5678", "CREDIT (*5678)"},
		// The mask is the last 2 to 4 characters, not always 4.
		{"Checking", "56", "Checking (*56)"},
		// Plaid sometimes reports no mask at all; the name is all there is.
		{"Total Checking", "", "Total Checking"},
	}
	for _, tc := range cases {
		if got := accountLabel(tc.name, tc.mask); got != tc.want {
			t.Errorf("accountLabel(%q, %q) = %q, want %q", tc.name, tc.mask, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// File creation and the two-phase write
// ---------------------------------------------------------------------------

func TestCreateExclusive_CreatesANewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.ofx")

	f, err := createExclusive(path)
	if err != nil {
		t.Fatalf("createExclusive: %v", err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
}

// os.Create would truncate here, and the transactions in the existing file
// would be gone while the caller went on to advance the cursor past them.
func TestCreateExclusive_RefusesAnExistingFileWithoutTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taken.ofx")
	if err := os.WriteFile(path, []byte("a statement that matters"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := createExclusive(path)
	if err == nil {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		t.Fatal("expected a refusal when the file already exists")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("err = %v, want it to wrap os.ErrExist", err)
	}
	if f != nil {
		t.Error("expected a nil WriteCloser alongside the error")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "a statement that matters" {
		t.Errorf("content = %q; the existing file was modified", got)
	}
}

func TestPathExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "there.ofx")

	exists, err := pathExists(path)
	if err != nil {
		t.Fatalf("pathExists on a free name: %v", err)
	}
	if exists {
		t.Error("expected false for a name nothing occupies")
	}

	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	exists, err = pathExists(path)
	if err != nil {
		t.Fatalf("pathExists on a taken name: %v", err)
	}
	if !exists {
		t.Error("expected true for a name a file occupies")
	}
}

// writePending creates a .part file holding body, and returns the result the
// exporter would have produced for it.
func writePending(t *testing.T, dir, finalName, body string) ofxexport.AccountResult {
	t.Helper()
	final := filepath.Join(dir, finalName)
	pending := final + ".part"
	if err := os.WriteFile(pending, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return ofxexport.AccountResult{FilePath: final, PendingPath: pending}
}

func TestCommitFiles_RenamesEveryPendingFile(t *testing.T) {
	dir := t.TempDir()
	results := []ofxexport.AccountResult{
		writePending(t, dir, "Chase_1234_20260716120000.ofx", "one"),
		{AccountName: "skipped, nothing written"},
		writePending(t, dir, "Chase_5678_20260716120000.ofx", "two"),
	}

	renamed, err := commitFiles(results)
	if err != nil {
		t.Fatalf("commitFiles: %v", err)
	}
	if len(renamed) != 2 {
		t.Errorf("renamed %d files, want 2", len(renamed))
	}

	for _, r := range results {
		if r.PendingPath == "" {
			continue
		}
		if _, err := os.Stat(r.FilePath); err != nil {
			t.Errorf("%s was not renamed into place: %v", r.FilePath, err)
		}
		if _, err := os.Stat(r.PendingPath); !os.IsNotExist(err) {
			t.Errorf("%s still exists after the rename", r.PendingPath)
		}
	}
}

// A rename that fails leaves nothing behind: the files that already landed
// are removed and the rest are discarded, so the caller never commits a
// cursor over a partial set.
func TestCommitFiles_AFailedRenameUndoesTheOnesBeforeIt(t *testing.T) {
	dir := t.TempDir()
	first := writePending(t, dir, "Chase_1234_20260716120000.ofx", "one")
	doomed := writePending(t, dir, "Chase_5678_20260716120000.ofx", "two")
	third := writePending(t, dir, "Chase_9012_20260716120000.ofx", "three")

	// A directory already standing on the target name makes its rename fail.
	if err := os.Mkdir(doomed.FilePath, 0o755); err != nil {
		t.Fatal(err)
	}

	renamed, err := commitFiles([]ofxexport.AccountResult{first, doomed, third})
	if err == nil {
		t.Fatal("expected an error when a rename fails")
	}
	if renamed != nil {
		t.Errorf("renamed = %v, want nil on failure", renamed)
	}

	if _, err := os.Stat(first.FilePath); !os.IsNotExist(err) {
		t.Error("the first file landed and was not undone")
	}
	for _, r := range []ofxexport.AccountResult{first, doomed, third} {
		if _, err := os.Stat(r.PendingPath); !os.IsNotExist(err) {
			t.Errorf("%s was left behind", r.PendingPath)
		}
	}
}

func TestPendingPaths_SkipsResultsThatWroteNothing(t *testing.T) {
	results := []ofxexport.AccountResult{
		{PendingPath: "a.ofx.part", FilePath: "a.ofx"},
		{AccountName: "skipped"},
		{PendingPath: "b.ofx.part", FilePath: "b.ofx"},
	}

	got := pendingPaths(results)
	want := []string{"a.ofx.part", "b.ofx.part"}
	if !slices.Equal(got, want) {
		t.Errorf("pendingPaths = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// The exporter and the rename, together, over a real directory
// ---------------------------------------------------------------------------

type nothingExported struct{}

func (nothingExported) IsExported(string) (bool, error) { return false, nil }

// testExporter pins the clock. Filenames carry the second they were written
// in, so a real clock would let two accounts straddle a second boundary and
// stop colliding — the collision test below would pass for the wrong reason,
// rarely and unreproducibly.
func testExporter(dir string) *ofxexport.Exporter {
	return &ofxexport.Exporter{
		Store:      nothingExported{},
		OutputDir:  dir,
		CreateFile: createExclusive,
		Exists:     pathExists,
		Now:        func() time.Time { return time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC) },
	}
}

func testAccount(id, mask string) source.Account {
	return source.Account{
		ID:          id,
		Name:        "CREDIT",
		Type:        source.Credit,
		Subtype:     source.CreditCard,
		Currency:    money.USD,
		LastFour:    mask,
		Institution: source.Institution{ID: "ins_128026", Name: "Capital One"},
	}
}

func testTxn(id, acctID string) source.Transaction {
	return source.Transaction{
		ID:          id,
		AccountID:   acctID,
		Amount:      money.MustParse("25.00", money.USD),
		Date:        civildate.MustNew(2026, time.July, 3),
		Description: "Coffee",
	}
}

func ofxFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// The ordinary case: two accounts on one login, told apart by their masks.
// Both statements land under their final names, and no .part file survives.
func TestExportThenCommit_WritesEveryAccountIntoPlace(t *testing.T) {
	dir := t.TempDir()
	accounts := []source.Account{testAccount("acc_1", "1234"), testAccount("acc_2", "5678")}
	byAccount := map[string][]source.Transaction{
		"acc_1": {testTxn("txn_1", "acc_1")},
		"acc_2": {testTxn("txn_2", "acc_2")},
	}

	results := testExporter(dir).ExportAll(accounts, byAccount)
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("account %d: %v", i, r.Err)
		}
	}

	// Before the rename, nothing an import would pick up exists yet.
	for _, name := range ofxFilesIn(t, dir) {
		if !strings.HasSuffix(name, ".part") {
			t.Errorf("%s is visible before the rename", name)
		}
	}

	renamed, err := commitFiles(results)
	if err != nil {
		t.Fatalf("commitFiles: %v", err)
	}
	if len(renamed) != 2 {
		t.Fatalf("renamed %d files, want 2", len(renamed))
	}

	names := ofxFilesIn(t, dir)
	if len(names) != 2 {
		t.Fatalf("directory holds %v, want exactly the two statements", names)
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".ofx") {
			t.Errorf("%s was left behind; want only .ofx files", name)
		}
	}

	// Each file is a statement GnuCash could read, for the account it names.
	for _, path := range renamed {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		stmt, rerr := ofx.Read(f)
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close %s: %v", path, cerr)
		}
		if rerr != nil {
			t.Fatalf("read back %s: %v", path, rerr)
		}
		if stmt.CreditCard == nil {
			t.Fatalf("%s is not a credit card statement", path)
		}
		if !strings.Contains(filepath.Base(path), stmt.CreditCard.Account.AccountID) {
			t.Errorf("%s does not name the account it holds (%s)",
				filepath.Base(path), stmt.CreditCard.Account.AccountID)
		}
	}
}

// Two accounts that resolve to one filename — same institution, same mask,
// same second. This is what silently destroyed a statement before: the second
// write truncated the first, and both accounts' transactions were still
// recorded as exported. It must refuse instead, with the first file intact.
func TestExportThenCommit_RefusesTwoAccountsSharingAFilename(t *testing.T) {
	dir := t.TempDir()
	accounts := []source.Account{testAccount("acc_1", "1234"), testAccount("acc_2", "1234")}
	byAccount := map[string][]source.Transaction{
		"acc_1": {testTxn("txn_1", "acc_1")},
		"acc_2": {testTxn("txn_2", "acc_2")},
	}

	results := testExporter(dir).ExportAll(accounts, byAccount)

	if results[0].Err != nil {
		t.Fatalf("the first account should export: %v", results[0].Err)
	}
	if results[1].Err == nil {
		t.Fatal("the second account collides and must be refused")
	}
	if !errors.Is(results[1].Err, os.ErrExist) {
		t.Errorf("Err = %v, want it to wrap os.ErrExist", results[1].Err)
	}

	// The first account's statement is untouched, and still pending: the
	// caller abandons the whole Item, so it is removed rather than renamed.
	pending := pendingPaths(results)
	if len(pending) != 1 || pending[0] != results[0].PendingPath {
		t.Fatalf("pendingPaths = %v, want only the first account's file", pending)
	}
	if _, err := os.Stat(results[0].PendingPath); err != nil {
		t.Errorf("the first account's statement was damaged: %v", err)
	}

	removeAll(pending)
	if names := ofxFilesIn(t, dir); len(names) != 0 {
		t.Errorf("directory holds %v after abandoning the item, want nothing", names)
	}
}

// Two logins at one bank are two Items with the same institution name. A run
// that printed only the name would report both of them identically.
func TestItemLabel_DistinguishesTwoLoginsAtOneBank(t *testing.T) {
	first := plaid.Item{ItemID: "item_1", InstitutionName: "Capital One"}
	second := plaid.Item{ItemID: "item_2", InstitutionName: "Capital One"}

	if itemLabel(first) == itemLabel(second) {
		t.Errorf("both logins print as %q", itemLabel(first))
	}
	if got := itemLabel(first); got != "Capital One (item item_1)" {
		t.Errorf("itemLabel = %q", got)
	}
}
