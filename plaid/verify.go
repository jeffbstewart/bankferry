package plaid

import "sort"

// VerifyStatus is the outcome of comparing one Item between a backup file
// and the keyring.
type VerifyStatus string

const (
	// VerifyMatch means the backup would restore this Item exactly.
	VerifyMatch VerifyStatus = "match"

	// VerifyTokenDiffers means the access token in the backup is not the one
	// in the keyring. The backup is stale, or the token was rotated. A
	// restore would install a token that may no longer authenticate.
	VerifyTokenDiffers VerifyStatus = "access token differs"

	// VerifyMetadataDiffers means the token matches but the institution
	// details do not. Harmless to restore, but the backup is out of date.
	VerifyMetadataDiffers VerifyStatus = "institution details differ"

	// VerifyOnlyInBackup means the keyring has no such Item. Either the Item
	// was deleted locally, or the keyring has already lost it and the backup
	// is the only surviving copy.
	VerifyOnlyInBackup VerifyStatus = "present in the backup, absent from the keyring"

	// VerifyOnlyInKeyring means the Item was linked after the backup was
	// taken. Its access token exists nowhere else. This is the dangerous one.
	VerifyOnlyInKeyring VerifyStatus = "present in the keyring, absent from the backup"
)

// ItemVerification is one line of a verification report. It never carries an
// access token.
type ItemVerification struct {
	ItemID          string
	InstitutionName string
	Status          VerifyStatus
}

// Covered reports whether a restore from this backup would preserve the Item.
func (v ItemVerification) Covered() bool {
	return v.Status == VerifyMatch || v.Status == VerifyMetadataDiffers
}

// VerifyBackup compares a decrypted backup against the Items currently in
// the keyring.
//
// The asymmetry matters. An Item in the backup but not the keyring is at
// worst untidy. An Item in the keyring but not the backup is unprotected:
// its access token exists in exactly one place, and Plaid will not reissue
// it. That is the case the report must make impossible to overlook.
func VerifyBackup(backup, keyring []Item) []ItemVerification {
	fromBackup := indexByItemID(backup)
	fromKeyring := indexByItemID(keyring)

	var report []ItemVerification

	for id, b := range fromBackup {
		k, present := fromKeyring[id]
		if !present {
			report = append(report, ItemVerification{
				ItemID:          id,
				InstitutionName: b.InstitutionName,
				Status:          VerifyOnlyInBackup,
			})
			continue
		}

		switch {
		case b.AccessToken != k.AccessToken:
			report = append(report, ItemVerification{
				ItemID:          id,
				InstitutionName: k.InstitutionName,
				Status:          VerifyTokenDiffers,
			})
		case b.InstitutionID != k.InstitutionID || b.InstitutionName != k.InstitutionName:
			report = append(report, ItemVerification{
				ItemID:          id,
				InstitutionName: k.InstitutionName,
				Status:          VerifyMetadataDiffers,
			})
		default:
			report = append(report, ItemVerification{
				ItemID:          id,
				InstitutionName: k.InstitutionName,
				Status:          VerifyMatch,
			})
		}
	}

	for id, k := range fromKeyring {
		if _, present := fromBackup[id]; !present {
			report = append(report, ItemVerification{
				ItemID:          id,
				InstitutionName: k.InstitutionName,
				Status:          VerifyOnlyInKeyring,
			})
		}
	}

	sort.Slice(report, func(i, j int) bool { return report[i].ItemID < report[j].ItemID })
	return report
}

// UnprotectedItems returns the Items whose access token exists only in the
// keyring, and would be lost with the machine.
func UnprotectedItems(report []ItemVerification) []ItemVerification {
	var out []ItemVerification
	for _, v := range report {
		if v.Status == VerifyOnlyInKeyring {
			out = append(out, v)
		}
	}
	return out
}

// BackupIsFaithful reports whether every keyring Item is covered by the
// backup with a matching access token.
func BackupIsFaithful(report []ItemVerification) bool {
	for _, v := range report {
		switch v.Status {
		case VerifyOnlyInKeyring, VerifyTokenDiffers:
			return false
		}
	}
	return true
}

func indexByItemID(items []Item) map[string]Item {
	index := make(map[string]Item, len(items))
	for _, item := range items {
		index[item.ItemID] = item
	}
	return index
}
