package plaid

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jeffbstewart/bankferry/secrets"
)

// backupStateVersion is the schema of the recorded backup state.
const backupStateVersion = 1

func backupStateKey(env Environment) string {
	return "plaid-backup-state-" + string(env)
}

// BackupState records what the last export covered. It is not a secret: the
// fingerprint is a one-way digest, and the path and timestamp are metadata.
// It lives in the keyring only because that is where the Items it describes
// live, so the two cannot drift onto different machines.
type BackupState struct {
	Version     int       `json:"version"`
	Fingerprint string    `json:"fingerprint"`
	ExportedAt  time.Time `json:"exported_at"`
	Path        string    `json:"path"`
}

// FingerprintItems digests the Items that a backup must cover.
//
// The access token is part of the digest, so a rotated token makes an old
// backup stale even when the set of Items is unchanged. The digest is
// one-way, so recording it leaks nothing.
func FingerprintItems(items []Item) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.ItemID+"\x00"+item.AccessToken)
	}
	sort.Strings(lines)

	h := sha256.New()
	for _, line := range lines {
		h.Write([]byte(line))
		h.Write([]byte("\x01"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RecordBackup remembers that these Items were exported to path.
func RecordBackup(env Environment, path string, items []Item) error {
	state := BackupState{
		Version:     backupStateVersion,
		Fingerprint: FingerprintItems(items),
		ExportedAt:  time.Now().UTC(),
		Path:        path,
	}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("plaid: encoding backup state: %w", err)
	}

	return storeSecret(backupStateKey(env), data,
		fmt.Sprintf("Plaid %s backup state", env),
		fmt.Sprintf("When the %s items were last exported, and what they were", env))
}

// LoadBackupState reads the recorded state. The boolean is false when no
// export has ever been recorded.
func LoadBackupState(env Environment) (BackupState, bool, error) {
	data, err := loadSecret(backupStateKey(env))
	if errors.Is(err, secrets.ErrNotFound) {
		return BackupState{}, false, nil
	}
	if err != nil {
		return BackupState{}, false, err
	}

	var state BackupState
	if err := json.Unmarshal(data, &state); err != nil {
		return BackupState{}, false, fmt.Errorf("plaid: parsing backup state: %w", err)
	}
	if state.Version != backupStateVersion {
		// An unrecognized record is treated as no record: the worst outcome
		// is a spurious reminder to take a backup.
		return BackupState{}, false, nil
	}
	return state, true, nil
}

// BackupWarning is a reminder that the Items on this machine are not covered
// by any export. Empty means nothing to say.
type BackupWarning struct {
	Environment Environment
	ItemCount   int

	// NeverExported is true when no export has ever been recorded.
	NeverExported bool

	// LastExport is when the covering export was taken, when one exists.
	LastExport time.Time

	// PreviousPath is where that export was written. It lets the tool name
	// a concrete destination rather than a placeholder, since exports never
	// overwrite and the next file must go somewhere new.
	PreviousPath string
}

// SuggestedPath is where the next export should go: alongside the previous
// one, dated, so it never collides with it. Empty when no previous export is
// known.
func (w BackupWarning) SuggestedPath() string {
	if w.PreviousPath == "" {
		return ""
	}
	return DatedBackupPath(w.PreviousPath, time.Now())
}

// DatedBackupPath derives a dated sibling of a previous backup path, so an
// export never has to overwrite one.
func DatedBackupPath(previous string, now time.Time) string {
	dir := filepath.Dir(previous)
	ext := filepath.Ext(previous)
	base := strings.TrimSuffix(filepath.Base(previous), ext)

	// Strip a trailing -YYYYMMDD so repeated exports do not accumulate dates.
	if len(base) > 9 {
		if tail := base[len(base)-9:]; tail[0] == '-' && allDigits(tail[1:]) {
			base = base[:len(base)-9]
		}
	}

	return filepath.Join(dir, fmt.Sprintf("%s-%s%s", base, now.Format("20060102"), ext))
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// SuggestedCommand is the complete command that would bring the backup up to
// date. It falls back to a placeholder when no destination can be inferred.
func (w BackupWarning) SuggestedCommand() string {
	out := w.SuggestedPath()
	if out == "" {
		return fmt.Sprintf("bankferry plaid-export --env %s --out <file>", w.Environment)
	}
	return fmt.Sprintf("bankferry plaid-export --env %s --out %s", w.Environment, out)
}

func (w BackupWarning) Message() string {
	if w.NeverExported {
		return fmt.Sprintf(
			"%d %s item(s) have never been exported. Their access tokens exist only in\n"+
				"this machine's keyring and Plaid will not reissue them.\n"+
				"  %s",
			w.ItemCount, w.Environment, w.SuggestedCommand())
	}
	return fmt.Sprintf(
		"%s items have changed since the last export on %s (%s).\n"+
			"Access tokens added or rotated since then exist only in this machine's keyring.\n"+
			"  %s",
		w.Environment, w.LastExport.Local().Format("2006-01-02"), w.PreviousPath,
		w.SuggestedCommand())
}

// CheckBackup reports whether the environment's Items are covered by the
// recorded export. It returns a nil warning when there is nothing to say:
// no Items, or a backup whose fingerprint still matches.
//
// Coverage is judged from the recorded fingerprint alone; this deliberately
// does not open the backup file or re-decrypt it. That is verify's job, and it
// is intentional here: the expected case is a backup written to removable media
// that is normally absent, where checking the file would report a false gap
// every time the drive is unplugged. So "covered" means the fingerprint of what
// was exported still matches the Items — not that the file exists on disk now.
//
// It never fails the caller. A keyring that cannot be read is reported as an
// error for the caller to ignore, because nagging is not worth breaking a
// command over.
func CheckBackup(env Environment) (*BackupWarning, error) {
	items, _, err := LoadItems(env)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}

	state, found, err := LoadBackupState(env)
	if err != nil {
		return nil, err
	}
	if !found {
		return &BackupWarning{
			Environment:   env,
			ItemCount:     len(items),
			NeverExported: true,
		}, nil
	}

	if state.Fingerprint == FingerprintItems(items) {
		return nil, nil
	}

	return &BackupWarning{
		Environment:  env,
		ItemCount:    len(items),
		LastExport:   state.ExportedAt,
		PreviousPath: state.Path,
	}, nil
}

// AllEnvironments is every environment whose Items might need backing up.
func AllEnvironments() []Environment {
	return []Environment{Sandbox, Production}
}
