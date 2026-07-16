package cli

import (
	"fmt"
	"log"
	"os"
)

func stdout(format string, args ...any) {
	if _, err := fmt.Fprintf(os.Stdout, format, args...); err != nil {
		log.Fatalf("failed to write to stdout: %v", err)
	}
}

func stderr(format string, args ...any) {
	if _, err := fmt.Fprintf(os.Stderr, format, args...); err != nil {
		log.Fatalf("failed to write to stderr: %v", err)
	}
}
