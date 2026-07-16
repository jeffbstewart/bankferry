package cli

import (
	"testing"
	"time"

	"github.com/jeffbstewart/bankferry/civildate"
	"github.com/jeffbstewart/bankferry/plaid"
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
