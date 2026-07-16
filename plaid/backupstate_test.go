package plaid

import (
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fingerprint
// ---------------------------------------------------------------------------

func TestFingerprintItems_IgnoresOrder(t *testing.T) {
	a := []Item{item("i1", "t1", "ins1", "A"), item("i2", "t2", "ins2", "B")}
	b := []Item{item("i2", "t2", "ins2", "B"), item("i1", "t1", "ins1", "A")}

	if FingerprintItems(a) != FingerprintItems(b) {
		t.Error("the fingerprint must not depend on item order")
	}
}

// A rotated access token makes an existing backup stale even though the set
// of Items is unchanged. That is the point of hashing the token.
func TestFingerprintItems_ChangesWithToken(t *testing.T) {
	before := []Item{item("i1", "old", "ins1", "A")}
	after := []Item{item("i1", "new", "ins1", "A")}

	if FingerprintItems(before) == FingerprintItems(after) {
		t.Error("a rotated token must change the fingerprint")
	}
}

func TestFingerprintItems_ChangesWithNewItem(t *testing.T) {
	before := []Item{item("i1", "t1", "ins1", "A")}
	after := append(before, item("i2", "t2", "ins2", "B"))

	if FingerprintItems(before) == FingerprintItems(after) {
		t.Error("a new item must change the fingerprint")
	}
}

// Institution names are cosmetic; renaming one must not force a re-export.
func TestFingerprintItems_IgnoresInstitutionName(t *testing.T) {
	a := []Item{item("i1", "t1", "ins1", "Bank of America")}
	b := []Item{item("i1", "t1", "ins1", "BofA")}

	if FingerprintItems(a) != FingerprintItems(b) {
		t.Error("a cosmetic rename must not make the backup stale")
	}
}

// The digest is one-way: the recorded state must not reveal a token.
func TestFingerprintItems_DoesNotContainTheToken(t *testing.T) {
	fp := FingerprintItems([]Item{item("i1", "super-secret-token", "ins1", "A")})
	if len(fp) != 64 {
		t.Errorf("fingerprint length = %d, want a 64-char sha256 hex digest", len(fp))
	}
	if fp == "super-secret-token" || len(fp) == 0 {
		t.Fatal("the fingerprint leaks the token")
	}
}

// ---------------------------------------------------------------------------
// CheckBackup
// ---------------------------------------------------------------------------

func TestCheckBackup_NoItemsIsSilent(t *testing.T) {
	useFakeItemStore(t)

	warning, err := CheckBackup(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if warning != nil {
		t.Errorf("nothing linked should produce no warning, got %+v", warning)
	}
}

func TestCheckBackup_NeverExported(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, item("i1", "t1", "ins1", "A")); err != nil {
		t.Fatal(err)
	}

	warning, err := CheckBackup(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if warning == nil || !warning.NeverExported {
		t.Fatalf("warning = %+v, want NeverExported", warning)
	}
	if warning.ItemCount != 1 {
		t.Errorf("item count = %d", warning.ItemCount)
	}
	// With no previous path to build on, the command stays a placeholder.
	if warning.SuggestedPath() != "" {
		t.Error("no previous path means no suggested path")
	}
}

func TestCheckBackup_UpToDateIsSilent(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, item("i1", "t1", "ins1", "A")); err != nil {
		t.Fatal(err)
	}
	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordBackup(Sandbox, "/backups/plaid.tapb", items); err != nil {
		t.Fatal(err)
	}

	warning, err := CheckBackup(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if warning != nil {
		t.Errorf("a current backup should be silent, got %+v", warning)
	}
}

// The nag that matters: an Item linked after the last export.
func TestCheckBackup_StaleAfterNewItem(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, item("i1", "t1", "ins1", "A")); err != nil {
		t.Fatal(err)
	}
	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordBackup(Sandbox, "/backups/plaid.tapb", items); err != nil {
		t.Fatal(err)
	}

	if err := SaveItem(Sandbox, item("i2", "t2", "ins2", "B")); err != nil {
		t.Fatal(err)
	}

	warning, err := CheckBackup(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if warning == nil {
		t.Fatal("a new item must make the backup stale")
	}
	if warning.NeverExported {
		t.Error("an export was recorded; this is staleness, not absence")
	}
	if warning.PreviousPath != "/backups/plaid.tapb" {
		t.Errorf("previous path = %q", warning.PreviousPath)
	}
	if warning.LastExport.IsZero() {
		t.Error("the last export time was not recorded")
	}
}

// A rotated token also makes the backup stale.
func TestCheckBackup_StaleAfterTokenRotation(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, item("i1", "old", "ins1", "A")); err != nil {
		t.Fatal(err)
	}
	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordBackup(Sandbox, "/backups/plaid.tapb", items); err != nil {
		t.Fatal(err)
	}

	if err := ReplaceItem(Sandbox, item("i1", "rotated", "ins1", "A")); err != nil {
		t.Fatal(err)
	}

	warning, err := CheckBackup(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if warning == nil {
		t.Fatal("a rotated token must make the backup stale")
	}
}

// Environments are tracked independently.
func TestCheckBackup_EnvironmentsAreIndependent(t *testing.T) {
	useFakeItemStore(t)
	if err := SaveItem(Sandbox, item("i1", "t1", "ins1", "A")); err != nil {
		t.Fatal(err)
	}
	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordBackup(Sandbox, "/backups/sandbox.tapb", items); err != nil {
		t.Fatal(err)
	}

	if err := SaveItem(Production, item("p1", "pt1", "ins9", "Real")); err != nil {
		t.Fatal(err)
	}

	if w, err := CheckBackup(Sandbox); err != nil || w != nil {
		t.Errorf("sandbox should be current: %+v %v", w, err)
	}
	w, err := CheckBackup(Production)
	if err != nil {
		t.Fatal(err)
	}
	if w == nil || !w.NeverExported {
		t.Errorf("production has never been exported: %+v", w)
	}
}

// An unreadable state record is treated as no record: at worst a spurious
// reminder, never a silent gap.
func TestLoadBackupState_UnknownVersionIsTreatedAsAbsent(t *testing.T) {
	f := useFakeItemStore(t)
	f.items[backupStateKey(Sandbox)] = []byte(`{"version":99,"fingerprint":"x"}`)

	_, found, err := LoadBackupState(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("an unknown state version must be treated as absent")
	}
}

// ---------------------------------------------------------------------------
// Suggested path
// ---------------------------------------------------------------------------

func TestDatedBackupPath(t *testing.T) {
	now := time.Date(2026, time.July, 9, 0, 0, 0, 0, time.UTC)

	cases := []struct{ previous, want string }{
		{filepath.Join("/backups", "plaid.tapb"), filepath.Join("/backups", "plaid-20260709.tapb")},
		{filepath.Join("/backups", "plaid-20260101.tapb"), filepath.Join("/backups", "plaid-20260709.tapb")},
		{filepath.Join("/b", "no-extension"), filepath.Join("/b", "no-extension-20260709")},
	}
	for _, tc := range cases {
		if got := DatedBackupPath(tc.previous, now); got != tc.want {
			t.Errorf("DatedBackupPath(%q) = %q, want %q", tc.previous, got, tc.want)
		}
	}
}

// A dated path never collides with the file it was derived from, because an
// export refuses to overwrite.
func TestDatedBackupPath_DiffersFromPrevious(t *testing.T) {
	now := time.Date(2026, time.July, 9, 0, 0, 0, 0, time.UTC)
	previous := filepath.Join("/backups", "plaid.tapb")
	if DatedBackupPath(previous, now) == previous {
		t.Error("the suggested path must differ from the previous one")
	}
}

func TestBackupWarning_SuggestedCommand(t *testing.T) {
	w := BackupWarning{Environment: Sandbox, PreviousPath: filepath.Join("/b", "plaid.tapb")}
	got := w.SuggestedCommand()
	if got == "" || !containsAll(got, "plaid-export", "--env sandbox", "--out") {
		t.Errorf("command = %q", got)
	}

	none := BackupWarning{Environment: Sandbox}
	if none.SuggestedCommand() == "" {
		t.Error("a warning without a previous path still names the command")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
