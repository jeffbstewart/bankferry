package plaid

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeffbstewart/bankferry/secrets"
)

// Item is one linked institution.
//
// An Item's access_token is unrecoverable. Plaid has no endpoint that
// returns an access_token given an item_id: /item/get returns status
// only, and /item/access_token/invalidate rotates a token you already
// hold. /item/remove also requires the access_token. So losing the token
// leaves an Item that can be neither read nor deleted, and re-linking the
// institution creates a *duplicate* Item rather than recovering the old
// one — permanently consuming another of the Trial plan's ten Items.
//
// Every storage decision here follows from that: one keyring entry per
// Item so corruption cannot spread, no index entry whose loss would
// strand the Items it points at, no read-modify-write of a shared blob,
// and a read-back check before any caller is told the token is safe.
//
// The keyring is machine-local and is not backed up. Moving to another
// computer, reinstalling the OS, or losing the disk strands every Item,
// and each re-link consumes another of the Trial plan's ten. An export
// and import path must exist before the first production Item is linked;
// afterwards there is nothing left to export. See Plaid.md section 6.2.
type Item struct {
	// Version is the schema version of the stored JSON. SaveItem always
	// writes itemSchemaVersion; LoadItems refuses anything else rather
	// than guessing at a layout it does not know.
	Version int `json:"version"`

	ItemID          string `json:"item_id"`
	AccessToken     string `json:"access_token"`
	InstitutionID   string `json:"institution_id"`
	InstitutionName string `json:"institution_name"`
}

// itemSchemaVersion is the version this code writes and the only one it
// reads. Bump it when the stored shape changes, and teach LoadItems to
// migrate the older shape rather than reject it.
const itemSchemaVersion = 1

// ErrUnsupportedItemVersion reports a stored Item whose schema version
// this build does not understand — most likely written by a newer build.
// Such an entry is reported and left untouched, never rewritten: it holds
// an access token that cannot be recovered from Plaid if destroyed.
var ErrUnsupportedItemVersion = errors.New("plaid: unsupported item schema version")

// BrokenItem is a stored entry that could not be decoded. It is reported
// rather than skipped: silently dropping one would hide the loss of an
// irreplaceable access token.
type BrokenItem struct {
	Key string
	Err error
}

func itemKeyPrefix(env Environment) string {
	return "plaid-item-" + string(env) + "-"
}

func itemKey(env Environment, itemID string) string {
	return itemKeyPrefix(env) + itemID
}

// Indirection over the keyring so tests can substitute a fake.
var listSecrets = secrets.Keys

// LoadItems returns the Items stored for the environment, along with any
// entries that failed to decode. An empty list with no error and no
// broken entries means nothing has been linked yet.
//
// A corrupt entry never blocks the healthy ones: callers get what is
// readable plus an explicit account of what is not.
func LoadItems(env Environment) ([]Item, []BrokenItem, error) {
	keys, err := listSecrets(itemKeyPrefix(env))
	if err != nil {
		return nil, nil, err
	}

	var (
		items  []Item
		broken []BrokenItem
	)
	for _, key := range keys {
		data, err := loadSecret(key)
		if err != nil {
			broken = append(broken, BrokenItem{Key: key, Err: err})
			continue
		}

		var item Item
		if err := json.Unmarshal(data, &item); err != nil {
			broken = append(broken, BrokenItem{Key: key, Err: err})
			continue
		}
		if item.Version != itemSchemaVersion {
			broken = append(broken, BrokenItem{
				Key: key,
				Err: fmt.Errorf("%w: found %d, want %d",
					ErrUnsupportedItemVersion, item.Version, itemSchemaVersion),
			})
			continue
		}
		if item.AccessToken == "" || item.ItemID == "" {
			broken = append(broken, BrokenItem{
				Key: key,
				Err: errors.New("entry is missing item_id or access_token"),
			})
			continue
		}
		items = append(items, item)
	}

	return items, broken, nil
}

// ErrItemExists reports that an Item is already stored under the key,
// holding content that differs from what was offered. Rotating an access
// token is a deliberate act: use ReplaceItem.
var ErrItemExists = errors.New("plaid: an item is already stored under this key")

// ErrRefusingToClobber reports that the key holds an entry this build
// cannot decode — corrupt, or written by a newer schema version. It may
// still contain a live access token, and Plaid will not reissue one, so
// nothing overwrites it implicitly. Delete it explicitly if it is truly
// worthless.
var ErrRefusingToClobber = errors.New("plaid: refusing to overwrite an unreadable item entry")

// peekItem reports what is stored under an Item's key. A keyring failure
// is returned as an error and never reported as absence: treating a
// transient read failure as "nothing there" is exactly how a live token
// gets overwritten.
//
// The check and the write that follows it are not atomic; the keyring
// offers no compare-and-swap. That is acceptable here because a single
// person runs this tool from one terminal at a time. Two concurrent links
// of the same institution could still race.
func peekItem(env Environment, itemID string) (stored Item, exists, decodable bool, err error) {
	data, err := loadSecret(itemKey(env, itemID))
	if errors.Is(err, secrets.ErrNotFound) {
		return Item{}, false, false, nil
	}
	if err != nil {
		return Item{}, false, false, err
	}

	var item Item
	if err := json.Unmarshal(data, &item); err != nil {
		return Item{}, true, false, nil
	}
	if item.Version != itemSchemaVersion || item.ItemID == "" || item.AccessToken == "" {
		return item, true, false, nil
	}
	return item, true, true, nil
}

func validateItem(item Item) error {
	if item.ItemID == "" {
		return errors.New("plaid: item ID is empty")
	}
	if item.AccessToken == "" {
		return errors.New("plaid: access token is empty")
	}
	if strings.Contains(item.ItemID, "/") || strings.Contains(item.ItemID, "\\") {
		return fmt.Errorf("plaid: item ID %q contains a path separator", item.ItemID)
	}
	return nil
}

// SaveItem stores a newly linked Item. It refuses to overwrite anything:
// an existing entry with different content returns ErrItemExists, and an
// undecodable entry returns ErrRefusingToClobber. Storing the identical
// Item again succeeds and changes nothing, so a retried link is safe.
func SaveItem(env Environment, item Item) error {
	if err := validateItem(item); err != nil {
		return err
	}

	existing, exists, decodable, err := peekItem(env, item.ItemID)
	if err != nil {
		return fmt.Errorf("plaid: checking for an existing item %s: %w", item.ItemID, err)
	}

	if exists && !decodable {
		return fmt.Errorf("%w: %s", ErrRefusingToClobber, itemKey(env, item.ItemID))
	}
	if exists {
		if existing.AccessToken == item.AccessToken &&
			existing.InstitutionID == item.InstitutionID &&
			existing.InstitutionName == item.InstitutionName {
			return nil
		}
		return fmt.Errorf("%w: item %s (use ReplaceItem to rotate its access token)",
			ErrItemExists, item.ItemID)
	}

	return writeItem(env, item)
}

// ReplaceItem overwrites an existing Item, for a deliberate access-token
// rotation via /item/access_token/invalidate or a re-link through update
// mode, both of which keep the same item_id. It still refuses to destroy
// an entry it cannot decode.
func ReplaceItem(env Environment, item Item) error {
	if err := validateItem(item); err != nil {
		return err
	}

	_, exists, decodable, err := peekItem(env, item.ItemID)
	if err != nil {
		return fmt.Errorf("plaid: checking for an existing item %s: %w", item.ItemID, err)
	}
	if exists && !decodable {
		return fmt.Errorf("%w: %s", ErrRefusingToClobber, itemKey(env, item.ItemID))
	}

	return writeItem(env, item)
}

// writeItem serializes an Item, stores it, then reads it back and
// compares before returning. The read-back is not paranoia: the caller
// uses a nil return to decide it is safe to stop holding the only copy of
// an unrecoverable token.
func writeItem(env Environment, item Item) error {
	// The writer owns the version; a caller cannot forge one.
	item.Version = itemSchemaVersion

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("plaid: encoding item %s: %w", item.ItemID, err)
	}

	key := itemKey(env, item.ItemID)
	err = storeSecret(key, data,
		fmt.Sprintf("Plaid %s item: %s", env, item.InstitutionName),
		fmt.Sprintf("Plaid access token for %s in the %s environment", item.InstitutionName, env))
	if err != nil {
		return err
	}

	stored, err := loadSecret(key)
	if err != nil {
		return fmt.Errorf("plaid: item %s was written but could not be read back: %w", item.ItemID, err)
	}

	var check Item
	if err := json.Unmarshal(stored, &check); err != nil {
		return fmt.Errorf("plaid: item %s read back as invalid JSON: %w", item.ItemID, err)
	}
	if check.AccessToken != item.AccessToken || check.ItemID != item.ItemID {
		return fmt.Errorf("plaid: item %s did not survive the round trip to the keyring", item.ItemID)
	}
	if check.Version != itemSchemaVersion {
		return fmt.Errorf("plaid: item %s read back with version %d, want %d",
			item.ItemID, check.Version, itemSchemaVersion)
	}

	return nil
}

// FindItemByInstitution returns the stored Item for an institution, if
// any. Link creates a *duplicate* Item when the same institution is
// linked twice and an access token is requested, so callers check this
// before exchanging a public_token.
func FindItemByInstitution(env Environment, institutionID string) (Item, bool, error) {
	if institutionID == "" {
		return Item{}, false, nil
	}

	items, _, err := LoadItems(env)
	if err != nil {
		return Item{}, false, err
	}
	for _, item := range items {
		if item.InstitutionID == institutionID {
			return item, true, nil
		}
	}
	return Item{}, false, nil
}

// DeleteItem removes one Item's keyring entry. It does not call Plaid's
// /item/remove, so the Item continues to exist on Plaid's side — and
// because the access token is gone, it can never be removed afterwards.
// Call Plaid first.
func DeleteItem(env Environment, itemID string) error {
	return deleteSecret(itemKey(env, itemID))
}
