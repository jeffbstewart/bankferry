package plaid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// A backup is only as good as the guarantee that a future build can still
// read it. These tests are that guarantee.
//
// goldenPath holds a format-1 file, committed to the repository, encrypted
// under goldenPassphrase. It contains fabricated tokens, so publishing it
// costs nothing. Any change that makes this file unreadable, or changes what
// it decodes to, breaks every real backup ever taken and must fail loudly
// here before it reaches anyone's disk.

const (
	goldenPassphrase = "golden-test-passphrase"
	goldenPath       = "testdata/backup_v1_golden.tapb"
)

// goldenItems is exactly what goldenPath must decrypt to. Do not edit these
// values: the file on disk was encrypted from them and cannot be changed to
// match.
func goldenItems() []Item {
	return []Item{
		{Version: 1, ItemID: "item_golden_1", AccessToken: "access-sandbox-golden-aaa",
			InstitutionID: "ins_100", InstitutionName: "Golden Bank"},
		{Version: 1, ItemID: "item_golden_2", AccessToken: "access-sandbox-golden-bbb",
			InstitutionID: "ins_200", InstitutionName: "Second Golden Bank"},
	}
}

// TestGolden_RegenerateBackup rewrites the committed fixture. It is skipped
// unless PLAID_REGENERATE_GOLDEN is set, because regenerating it destroys the
// very property the other tests check: that a file written by an *older*
// build still reads.
//
// Regenerate only when deliberately introducing a new format, and then keep
// the old fixture and add a test that it still decrypts.
func TestGolden_RegenerateBackup(t *testing.T) {
	if os.Getenv("PLAID_REGENERATE_GOLDEN") == "" {
		t.Skip("set PLAID_REGENERATE_GOLDEN=1 to rewrite the committed fixture")
	}

	blob, err := EncryptItems(Sandbox, goldenItems(), []byte(goldenPassphrase))
	if err != nil {
		t.Fatalf("EncryptItems: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goldenPath, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes) — commit it", goldenPath, len(blob))
}

// The committed fixture must still decrypt, and to exactly what it held when
// it was written. This is the regression test for the file format.
func TestGolden_StillDecrypts(t *testing.T) {
	blob, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v (regenerate with PLAID_REGENERATE_GOLDEN=1)", goldenPath, err)
	}

	file, err := DecryptItems(blob, []byte(goldenPassphrase))
	if err != nil {
		t.Fatalf(`the committed format-1 backup no longer decrypts: %v

Every backup ever written by this tool is now unreadable. Either restore
compatibility, or add a migration path and keep this fixture readable.`, err)
	}

	if file.Format != 1 {
		t.Errorf("format = %d, want 1", file.Format)
	}
	if file.Environment != Sandbox {
		t.Errorf("environment = %q, want sandbox", file.Environment)
	}
	if file.ExportedAt.IsZero() || file.ExportedAt.After(time.Now()) {
		t.Errorf("exported_at = %v, which is not a sane past time", file.ExportedAt)
	}

	want := goldenItems()
	if len(file.Items) != len(want) {
		t.Fatalf("items = %d, want %d", len(file.Items), len(want))
	}
	for i := range want {
		if file.Items[i] != want[i] {
			t.Errorf(`item %d decoded differently than it was encoded:
  got:  %+v
  want: %+v
A field was added, removed, or renamed. Old backups will not restore it.`,
				i, file.Items[i], want[i])
		}
	}
}

// The environment must be readable from the committed fixture without a
// passphrase, since plaid-verify-backup relies on that to refuse a file from
// the wrong environment before prompting.
func TestGolden_EnvironmentReadableWithoutPassphrase(t *testing.T) {
	blob, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v", goldenPath, err)
	}

	env, err := BackupEnvironment(blob)
	if err != nil {
		t.Fatalf("BackupEnvironment: %v", err)
	}
	if env != Sandbox {
		t.Errorf("environment = %q, want sandbox", env)
	}
}

// The golden fixture is verifiable against a keyring holding the same Items.
func TestGolden_VerifiesAgainstMatchingKeyring(t *testing.T) {
	blob, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading %s: %v", goldenPath, err)
	}
	file, err := DecryptItems(blob, []byte(goldenPassphrase))
	if err != nil {
		t.Fatal(err)
	}

	report := VerifyBackup(file.Items, goldenItems())
	if !BackupIsFaithful(report) {
		t.Errorf("the golden backup does not verify against its own items: %+v", report)
	}
}

// Item is serialized into every backup and into every keyring entry. Its
// wire shape is therefore a compatibility surface, and changing it silently
// changes what a restore produces from a file already on disk.
//
// A field count alone would not catch a rename or a retag, both of which
// break every existing backup while leaving the count intact. So the JSON
// itself is pinned, byte for byte.
//
// If this fails, a field was added, removed, renamed, retagged, or reordered.
// Decide deliberately:
//
//   - If the change must survive a restore, bump itemSchemaVersion and teach
//     LoadItems to migrate the older shape.
//   - If it need not, update the literal below.
//
// Either way the committed golden fixture must keep decrypting as it always
// has. Never regenerate it to make a test pass.
func TestItem_JSONShapeIsPinned(t *testing.T) {
	sample := Item{
		Version:         1,
		ItemID:          "I",
		AccessToken:     "A",
		InstitutionID:   "N",
		InstitutionName: "M",
	}

	const pinned = `{"version":1,"item_id":"I","access_token":"A",` +
		`"institution_id":"N","institution_name":"M"}`

	got, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("marshaling Item: %v", err)
	}
	if string(got) != pinned {
		t.Fatalf(`the serialized shape of Item changed.

  got:  %s
  want: %s

Every backup and keyring entry already written uses the old shape. A field
was added, removed, renamed, retagged, or reordered. If the change must
survive a restore, bump itemSchemaVersion and migrate in LoadItems. Never
regenerate the golden fixture to make this pass.`, got, pinned)
	}

	// The count is pinned too, so an unexported field added for internal use
	// is noticed even though it does not appear in the JSON.
	const pinnedFields = 5
	if n := reflect.TypeOf(Item{}).NumField(); n != pinnedFields {
		t.Errorf("Item has %d fields, pinned at %d", n, pinnedFields)
	}
}

// The stored keyring entry and the backup entry are the same bytes. A
// backup written today must therefore be loadable as a keyring item, and an
// item stored today must be encryptable into a backup. Pinning one shape
// pins both.
func TestItem_BackupAndKeyringShapesAgree(t *testing.T) {
	original := goldenItems()[0]

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Item
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != original {
		t.Errorf("item did not survive a JSON round trip:\n  got:  %+v\n  want: %+v",
			decoded, original)
	}
}
