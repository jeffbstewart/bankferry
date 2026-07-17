package plaid

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

// useFakeStore already swaps storeSecret/loadSecret/deleteSecret.
// listSecrets needs the same treatment for the item tests.
func useFakeItemStore(t *testing.T) *fakeStore {
	t.Helper()

	f := useFakeStore(t)

	origList := listSecrets
	listSecrets = func(prefix string) ([]string, error) {
		if f.listErr != nil {
			return nil, f.listErr
		}
		var keys []string
		for k := range f.items {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		return keys, nil
	}
	t.Cleanup(func() { listSecrets = origList })

	return f
}

func testItem(id, instID, instName string) Item {
	return Item{
		ItemID:          id,
		AccessToken:     "access-" + id,
		InstitutionID:   instID,
		InstitutionName: instName,
	}
}

// ---------------------------------------------------------------------------
// SaveItem / LoadItems
// ---------------------------------------------------------------------------

func TestSaveLoadItems_RoundTrip(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err != nil {
		t.Fatalf("SaveItem: %v", err)
	}

	items, broken, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(broken) != 0 {
		t.Fatalf("broken = %v, want none", broken)
	}
	if len(items) != 1 || items[0].AccessToken != "access-item_1" {
		t.Fatalf("items = %+v", items)
	}
}

func TestLoadItems_EmptyIsNotAnError(t *testing.T) {
	useFakeItemStore(t)

	items, broken, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(items) != 0 || len(broken) != 0 {
		t.Errorf("items = %v, broken = %v, want both empty", items, broken)
	}
}

// Each Item lives in its own entry, so one corrupt entry must not hide or
// destroy the others. It is reported, never silently dropped.
func TestLoadItems_CorruptEntryDoesNotHideHealthyOnes(t *testing.T) {
	f := useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err != nil {
		t.Fatal(err)
	}
	if err := SaveItem(Sandbox, testItem("item_2", "ins_2", "Capital One")); err != nil {
		t.Fatal(err)
	}

	// Corrupt exactly one entry.
	f.items[itemKey(Sandbox, "item_1")] = []byte("{not json")

	items, broken, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(items) != 1 || items[0].ItemID != "item_2" {
		t.Errorf("items = %+v, want only item_2", items)
	}
	if len(broken) != 1 || broken[0].Key != itemKey(Sandbox, "item_1") {
		t.Errorf("broken = %+v, want item_1 reported", broken)
	}
}

// An entry that parses but carries no access token is unusable and must
// be reported, not returned as a healthy Item.
func TestLoadItems_EntryMissingTokenIsBroken(t *testing.T) {
	f := useFakeItemStore(t)

	f.items[itemKey(Sandbox, "item_1")] = []byte(`{"version":1,"item_id":"item_1","access_token":""}`)

	items, broken, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want none", items)
	}
	if len(broken) != 1 {
		t.Errorf("broken = %+v, want one", broken)
	}
}

// ---------------------------------------------------------------------------
// Schema version
// ---------------------------------------------------------------------------

func TestSaveItem_WritesSchemaVersion(t *testing.T) {
	f := useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err != nil {
		t.Fatal(err)
	}

	if got := string(f.items[itemKey(Sandbox, "item_1")]); !strings.Contains(got, `"version":1`) {
		t.Errorf("stored JSON = %s, want a version marker of 1", got)
	}
}

// The writer owns the version. A caller cannot smuggle in a different one.
func TestSaveItem_OverridesCallerSuppliedVersion(t *testing.T) {
	useFakeItemStore(t)

	item := testItem("item_1", "ins_1", "Chase")
	item.Version = 99

	if err := SaveItem(Sandbox, item); err != nil {
		t.Fatal(err)
	}

	items, broken, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(broken) != 0 {
		t.Fatalf("broken = %+v, want none", broken)
	}
	if len(items) != 1 || items[0].Version != itemSchemaVersion {
		t.Errorf("items = %+v, want version %d", items, itemSchemaVersion)
	}
}

// An entry written by a future build is reported, not treated as healthy.
func TestLoadItems_FutureVersionIsBroken(t *testing.T) {
	f := useFakeItemStore(t)

	f.items[itemKey(Sandbox, "item_1")] =
		[]byte(`{"version":2,"item_id":"item_1","access_token":"tok","institution_id":"ins_1"}`)

	items, broken, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want none", items)
	}
	if len(broken) != 1 {
		t.Fatalf("broken = %+v, want one", broken)
	}
	if !errors.Is(broken[0].Err, ErrUnsupportedItemVersion) {
		t.Errorf("err = %v, want ErrUnsupportedItemVersion", broken[0].Err)
	}
}

// An entry predating the version marker (version 0) is likewise refused
// rather than guessed at.
func TestLoadItems_MissingVersionIsBroken(t *testing.T) {
	f := useFakeItemStore(t)

	f.items[itemKey(Sandbox, "item_1")] =
		[]byte(`{"item_id":"item_1","access_token":"tok"}`)

	_, broken, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatalf("LoadItems: %v", err)
	}
	if len(broken) != 1 || !errors.Is(broken[0].Err, ErrUnsupportedItemVersion) {
		t.Errorf("broken = %+v, want ErrUnsupportedItemVersion", broken)
	}
}

// Refusing an unknown version must not destroy it: the entry holds an
// access token that Plaid will never hand back.
func TestLoadItems_UnknownVersionEntryIsLeftIntact(t *testing.T) {
	f := useFakeItemStore(t)

	raw := []byte(`{"version":2,"item_id":"item_1","access_token":"precious"}`)
	f.items[itemKey(Sandbox, "item_1")] = raw

	if _, _, err := LoadItems(Sandbox); err != nil {
		t.Fatal(err)
	}

	if got := string(f.items[itemKey(Sandbox, "item_1")]); got != string(raw) {
		t.Errorf("entry was modified: %s", got)
	}
}

// ---------------------------------------------------------------------------
// Clobber protection
// ---------------------------------------------------------------------------

// Saving the identical Item twice is a no-op, so retrying an interrupted
// link is safe.
func TestSaveItem_IdenticalRewriteIsANoOp(t *testing.T) {
	useFakeItemStore(t)

	item := testItem("item_1", "ins_1", "Chase")
	if err := SaveItem(Sandbox, item); err != nil {
		t.Fatal(err)
	}
	if err := SaveItem(Sandbox, item); err != nil {
		t.Fatalf("re-saving an identical item should succeed: %v", err)
	}

	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("items = %+v, want exactly one", items)
	}
}

// SaveItem never silently replaces a live token. Rotation is deliberate.
func TestSaveItem_RefusesToOverwriteDifferentToken(t *testing.T) {
	f := useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err != nil {
		t.Fatal(err)
	}

	rotated := testItem("item_1", "ins_1", "Chase")
	rotated.AccessToken = "different-token"

	err := SaveItem(Sandbox, rotated)
	if !errors.Is(err, ErrItemExists) {
		t.Fatalf("err = %v, want ErrItemExists", err)
	}

	// The original token must be untouched.
	if got := string(f.items[itemKey(Sandbox, "item_1")]); !strings.Contains(got, "access-item_1") {
		t.Errorf("stored entry was modified: %s", got)
	}
}

// An entry this build cannot decode may still hold a live token, so it is
// never overwritten implicitly.
func TestSaveItem_RefusesToClobberUndecodableEntry(t *testing.T) {
	f := useFakeItemStore(t)

	raw := []byte(`{"version":2,"item_id":"item_1","access_token":"precious"}`)
	f.items[itemKey(Sandbox, "item_1")] = raw

	err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase"))
	if !errors.Is(err, ErrRefusingToClobber) {
		t.Fatalf("err = %v, want ErrRefusingToClobber", err)
	}
	if got := string(f.items[itemKey(Sandbox, "item_1")]); got != string(raw) {
		t.Errorf("entry was modified: %s", got)
	}
}

func TestSaveItem_RefusesToClobberCorruptEntry(t *testing.T) {
	f := useFakeItemStore(t)

	raw := []byte("{not json")
	f.items[itemKey(Sandbox, "item_1")] = raw

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); !errors.Is(err, ErrRefusingToClobber) {
		t.Fatalf("err = %v, want ErrRefusingToClobber", err)
	}
	if got := string(f.items[itemKey(Sandbox, "item_1")]); got != string(raw) {
		t.Errorf("entry was modified: %s", got)
	}
}

// A transient keyring read failure must never be mistaken for absence.
// Treating it as "nothing stored" would overwrite a live token.
func TestSaveItem_KeyringReadFailureDoesNotWrite(t *testing.T) {
	f := useFakeItemStore(t)
	f.loadErr = errors.New("keyring locked")

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err == nil {
		t.Fatal("expected an error when the pre-write read fails")
	}
	if len(f.items) != 0 {
		t.Error("nothing may be written when the existence check fails")
	}
}

// ---------------------------------------------------------------------------
// ReplaceItem
// ---------------------------------------------------------------------------

// Rotation keeps the same item_id and is explicit.
func TestReplaceItem_RotatesAccessToken(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err != nil {
		t.Fatal(err)
	}

	rotated := testItem("item_1", "ins_1", "Chase")
	rotated.AccessToken = "rotated-token"
	if err := ReplaceItem(Sandbox, rotated); err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}

	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly one", items)
	}
	if items[0].AccessToken != "rotated-token" {
		t.Errorf("access token = %q, want the rotated one", items[0].AccessToken)
	}
}

// ReplaceItem may create an entry that does not exist yet.
func TestReplaceItem_CreatesWhenAbsent(t *testing.T) {
	useFakeItemStore(t)

	if err := ReplaceItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err != nil {
		t.Fatalf("ReplaceItem: %v", err)
	}
	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("items = %+v, want one", items)
	}
}

// Even an explicit rotation will not destroy an entry it cannot read,
// because that entry may hold a token Plaid will never reissue.
func TestReplaceItem_StillRefusesToClobberUndecodableEntry(t *testing.T) {
	f := useFakeItemStore(t)

	raw := []byte(`{"version":2,"item_id":"item_1","access_token":"precious"}`)
	f.items[itemKey(Sandbox, "item_1")] = raw

	if err := ReplaceItem(Sandbox, testItem("item_1", "ins_1", "Chase")); !errors.Is(err, ErrRefusingToClobber) {
		t.Fatalf("err = %v, want ErrRefusingToClobber", err)
	}
	if got := string(f.items[itemKey(Sandbox, "item_1")]); got != string(raw) {
		t.Errorf("entry was modified: %s", got)
	}
}

// Sandbox and production Items never share a namespace.
func TestItems_EnvironmentsAreIsolated(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_s", "ins_1", "Sandbox Bank")); err != nil {
		t.Fatal(err)
	}
	if err := SaveItem(Production, testItem("item_p", "ins_1", "Real Bank")); err != nil {
		t.Fatal(err)
	}

	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != "item_s" {
		t.Errorf("sandbox items = %+v", items)
	}
}

func TestSaveItem_RejectsEmptyFields(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, Item{AccessToken: "tok"}); err == nil {
		t.Error("expected an error for an empty item ID")
	}
	if err := SaveItem(Sandbox, Item{ItemID: "item_1"}); err == nil {
		t.Error("expected an error for an empty access token")
	}
}

// The read-back check must fail loudly if the keyring silently discards
// the write. The caller relies on a nil return to drop the only copy of
// an unrecoverable token.
func TestSaveItem_DetectsSilentlyLostWrite(t *testing.T) {
	f := useFakeItemStore(t)

	// A store that accepts writes and keeps nothing.
	origStore := storeSecret
	storeSecret = func(_ string, _ []byte, _, _ string) error { return nil }
	t.Cleanup(func() { storeSecret = origStore })

	err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase"))
	if err == nil {
		t.Fatal("expected an error when the write does not persist")
	}
	if len(f.items) != 0 {
		t.Fatal("test setup is wrong: nothing should have been stored")
	}
}

// A write that persists corrupted data must also be caught.
func TestSaveItem_DetectsCorruptedReadBack(t *testing.T) {
	f := useFakeItemStore(t)

	origStore := storeSecret
	storeSecret = func(key string, _ []byte, _, _ string) error {
		f.items[key] = []byte("{not json")
		return nil
	}
	t.Cleanup(func() { storeSecret = origStore })

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err == nil {
		t.Fatal("expected an error when the entry reads back as invalid JSON")
	}
}

func TestSaveItem_StoreError(t *testing.T) {
	f := useFakeItemStore(t)
	f.storeErr = errors.New("keyring locked")

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err == nil {
		t.Fatal("expected an error when the keyring write fails")
	}
}

// ---------------------------------------------------------------------------
// Duplicate-Item detection
// ---------------------------------------------------------------------------

// Linking the same institution twice creates a duplicate Item on Plaid's
// side and permanently consumes another of the Trial plan's ten. Callers
// must be able to detect it before exchanging the public token.
func TestFindItemsByInstitution(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_chase", "Chase")); err != nil {
		t.Fatal(err)
	}

	got, err := FindItemsByInstitution(Sandbox, "ins_chase")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ItemID != "item_1" {
		t.Errorf("got %+v, want just item_1", got)
	}

	got, err = FindItemsByInstitution(Sandbox, "ins_other")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v for an unlinked institution, want none", got)
	}
}

// An Item is one login, not one institution, so two logins at one bank are
// legitimately two Items. Reporting only the first would make the caller's
// choice of which one to name arbitrary.
func TestFindItemsByInstitution_ReturnsEveryMatch(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}
	if err := SaveItem(Sandbox, testItem("item_2", "ins_capone", "Capital One")); err != nil {
		t.Fatal(err)
	}
	if err := SaveItem(Sandbox, testItem("item_3", "ins_chase", "Chase")); err != nil {
		t.Fatal(err)
	}

	got, err := FindItemsByInstitution(Sandbox, "ins_capone")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want both logins at the institution: %+v", len(got), got)
	}
	ids := []string{got[0].ItemID, got[1].ItemID}
	sort.Strings(ids)
	if ids[0] != "item_1" || ids[1] != "item_2" {
		t.Errorf("got %v, want item_1 and item_2", ids)
	}
}

func TestFindItemsByInstitution_EmptyIDNeverMatches(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "", "Unknown")); err != nil {
		t.Fatal(err)
	}

	got, err := FindItemsByInstitution(Sandbox, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an empty institution ID must never match, got %+v", got)
	}
}

// ---------------------------------------------------------------------------
// DeleteItem
// ---------------------------------------------------------------------------

func TestDeleteItem_RemovesOnlyThatItem(t *testing.T) {
	useFakeItemStore(t)

	if err := SaveItem(Sandbox, testItem("item_1", "ins_1", "Chase")); err != nil {
		t.Fatal(err)
	}
	if err := SaveItem(Sandbox, testItem("item_2", "ins_2", "Capital One")); err != nil {
		t.Fatal(err)
	}

	if err := DeleteItem(Sandbox, "item_1"); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}

	items, _, err := LoadItems(Sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ItemID != "item_2" {
		t.Errorf("items = %+v, want only item_2", items)
	}
}
