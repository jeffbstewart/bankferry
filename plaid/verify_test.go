package plaid

import "testing"

func item(id, token, insID, insName string) Item {
	return Item{
		Version:         itemSchemaVersion,
		ItemID:          id,
		AccessToken:     token,
		InstitutionID:   insID,
		InstitutionName: insName,
	}
}

func statusOf(t *testing.T, report []ItemVerification, itemID string) VerifyStatus {
	t.Helper()
	for _, v := range report {
		if v.ItemID == itemID {
			return v.Status
		}
	}
	t.Fatalf("item %s missing from the report", itemID)
	return ""
}

func TestVerifyBackup_Match(t *testing.T) {
	items := []Item{item("item_1", "tok_1", "ins_1", "BofA")}

	report := VerifyBackup(items, items)
	if len(report) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report[0].Status != VerifyMatch {
		t.Errorf("status = %q, want match", report[0].Status)
	}
	if !BackupIsFaithful(report) {
		t.Error("an identical backup must be faithful")
	}
	if len(UnprotectedItems(report)) != 0 {
		t.Error("nothing should be unprotected")
	}
}

// A stale backup holding an old token would restore something that may no
// longer authenticate.
func TestVerifyBackup_TokenDiffers(t *testing.T) {
	backup := []Item{item("item_1", "old_token", "ins_1", "BofA")}
	keyring := []Item{item("item_1", "new_token", "ins_1", "BofA")}

	report := VerifyBackup(backup, keyring)
	if got := statusOf(t, report, "item_1"); got != VerifyTokenDiffers {
		t.Errorf("status = %q, want token differs", got)
	}
	if BackupIsFaithful(report) {
		t.Error("a backup with a different token is not faithful")
	}
}

// Metadata drift is harmless: the irreplaceable part still matches.
func TestVerifyBackup_MetadataDiffers(t *testing.T) {
	backup := []Item{item("item_1", "tok_1", "ins_1", "Bank of America")}
	keyring := []Item{item("item_1", "tok_1", "ins_1", "BofA")}

	report := VerifyBackup(backup, keyring)
	if got := statusOf(t, report, "item_1"); got != VerifyMetadataDiffers {
		t.Errorf("status = %q, want metadata differs", got)
	}
	if !BackupIsFaithful(report) {
		t.Error("a token that still matches means the backup is faithful")
	}
	if !report[0].Covered() {
		t.Error("the item is still covered by the backup")
	}
}

// The dangerous case: an Item linked after the backup was taken. Its access
// token exists in exactly one place and Plaid will not reissue it.
func TestVerifyBackup_OnlyInKeyringIsUnprotected(t *testing.T) {
	backup := []Item{item("item_1", "tok_1", "ins_1", "BofA")}
	keyring := []Item{
		item("item_1", "tok_1", "ins_1", "BofA"),
		item("item_2", "tok_2", "ins_2", "Capital One"),
	}

	report := VerifyBackup(backup, keyring)
	if got := statusOf(t, report, "item_2"); got != VerifyOnlyInKeyring {
		t.Errorf("status = %q, want only in keyring", got)
	}
	if BackupIsFaithful(report) {
		t.Error("an unbacked-up item means the backup is not faithful")
	}

	unprotected := UnprotectedItems(report)
	if len(unprotected) != 1 || unprotected[0].ItemID != "item_2" {
		t.Errorf("unprotected = %+v, want item_2", unprotected)
	}
	if unprotected[0].Covered() {
		t.Error("an item absent from the backup is not covered")
	}
}

// An Item in the backup but not the keyring is untidy, not dangerous: the
// token still exists somewhere.
func TestVerifyBackup_OnlyInBackupIsNotUnprotected(t *testing.T) {
	backup := []Item{
		item("item_1", "tok_1", "ins_1", "BofA"),
		item("item_gone", "tok_g", "ins_9", "Old Bank"),
	}
	keyring := []Item{item("item_1", "tok_1", "ins_1", "BofA")}

	report := VerifyBackup(backup, keyring)
	if got := statusOf(t, report, "item_gone"); got != VerifyOnlyInBackup {
		t.Errorf("status = %q, want only in backup", got)
	}
	if len(UnprotectedItems(report)) != 0 {
		t.Error("an item only in the backup is not unprotected")
	}
	if !BackupIsFaithful(report) {
		t.Error("an extra item in the backup does not make it unfaithful")
	}
}

func TestVerifyBackup_BothEmpty(t *testing.T) {
	report := VerifyBackup(nil, nil)
	if len(report) != 0 {
		t.Errorf("report = %+v, want empty", report)
	}
	if !BackupIsFaithful(report) {
		t.Error("an empty backup of an empty keyring is faithful")
	}
}

// Nothing in a verification report may carry an access token: the report is
// printed.
func TestItemVerification_CarriesNoToken(t *testing.T) {
	backup := []Item{item("item_1", "super_secret_token", "ins_1", "BofA")}
	keyring := []Item{item("item_1", "different_secret", "ins_1", "BofA")}

	for _, v := range VerifyBackup(backup, keyring) {
		if v.ItemID == "super_secret_token" || v.InstitutionName == "super_secret_token" {
			t.Fatal("a token leaked into the report")
		}
		if string(v.Status) == "super_secret_token" {
			t.Fatal("a token leaked into the status")
		}
	}
}

func TestVerifyBackup_IsSortedByItemID(t *testing.T) {
	items := []Item{
		item("item_c", "t", "i", "C"),
		item("item_a", "t", "i", "A"),
		item("item_b", "t", "i", "B"),
	}
	report := VerifyBackup(items, items)
	for i := 1; i < len(report); i++ {
		if report[i-1].ItemID > report[i].ItemID {
			t.Fatalf("report is not sorted: %v", report)
		}
	}
}
