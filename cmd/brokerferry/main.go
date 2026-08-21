// Command brokerferry pulls brokerage holdings and investment transactions
// from Plaid and writes the investments snapshot finance2 imports. It is one
// of two programs built from this module; see cli.App.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/jeffbstewart/bankferry/cli"
	"github.com/joho/godotenv"
)

func main() {
	// A missing .env is expected: configuration may come from the
	// environment instead. Any other failure is real and worth reporting.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "Error loading .env: %v\n", err)
		os.Exit(1)
	}
	cli.Run(cli.Brokerferry(), os.Args)
}
