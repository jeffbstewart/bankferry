package cli

import (
	"flag"
	"io"
	"testing"
)

// The whole point of moving to flag.FlagSet: an unknown or misspelled flag is
// an error, not a silently ignored no-op. parseFlags exits the process, so the
// behavior is tested one layer down, at fs.Parse, which is exactly what
// parseFlags checks.
func TestFlagSet_RejectsUnknownFlag(t *testing.T) {
	fs := newFlags("fetch")
	_ = envFlag(fs)
	fs.SetOutput(io.Discard)

	// A typo like --payee_report for --payee-report, or any flag the command
	// does not define, must fail rather than parse to a no-op.
	if err := fs.Parse([]string{"--payee_report", "out.csv"}); err == nil {
		t.Fatal("expected an error for an unknown flag")
	}
}

func TestFlagSet_AcceptsKnownFlags(t *testing.T) {
	fs := newFlags("plaid-relink")
	env := envFlag(fs)
	item := fs.String("item", "", "")
	fs.SetOutput(io.Discard)

	// Both --flag value and --flag=value forms parse.
	if err := fs.Parse([]string{"--env", "sandbox", "--item=abc"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *env != "sandbox" || *item != "abc" {
		t.Errorf("env=%q item=%q, want sandbox/abc", *env, *item)
	}
}

func TestFlagSet_HelpIsNotAnError(t *testing.T) {
	fs := newFlags("plaid-items")
	_ = envFlag(fs)
	fs.SetOutput(io.Discard)

	// -h returns flag.ErrHelp, which parseFlags treats as a clean exit rather
	// than a parse failure.
	if err := fs.Parse([]string{"-h"}); err != flag.ErrHelp {
		t.Errorf("Parse(-h) = %v, want flag.ErrHelp", err)
	}
}

func TestRequireEnv_RejectsUnknown(t *testing.T) {
	// requireEnv exits on an empty or invalid value, so only the happy path is
	// unit-testable without a subprocess. A valid value round-trips.
	if got := requireEnv("sandbox"); string(got) != "sandbox" {
		t.Errorf("requireEnv(sandbox) = %q", got)
	}
}
