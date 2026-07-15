// Package gnucash provides a read-only parser for GnuCash
// gzip-compressed XML data files. It extracts the distinct transaction
// descriptions (payees) used by the payee-learning system.
package gnucash

import (
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
)

// File represents the parsed contents of a GnuCash data file.
type File struct {
	// Payees holds every distinct, non-empty transaction description
	// found in the file, in no particular order.
	Payees []string
}

// Parse reads a GnuCash file at the given path and extracts transaction
// payees. The file may be gzip-compressed (the default GnuCash format)
// or plain XML.
func Parse(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gnucash file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			fmt.Printf("gnucash: close file: %v\n", cerr)
		}
	}()

	return ParseReader(f)
}

// ParseReader reads GnuCash XML from an io.Reader. It attempts gzip
// decompression first; if that fails it reads the data as plain XML.
// The reader must support seeking (or be a ReadSeeker) for the
// fallback to work, otherwise only gzip-compressed input is accepted.
func ParseReader(r io.Reader) (*File, error) {
	// Try gzip first.
	var xmlReader io.Reader
	if rs, ok := r.(io.ReadSeeker); ok {
		gz, err := gzip.NewReader(rs)
		if err != nil {
			// Not gzip — seek back and read as plain XML.
			if _, serr := rs.Seek(0, io.SeekStart); serr != nil {
				return nil, fmt.Errorf("seek after gzip probe: %w", serr)
			}
			xmlReader = rs
		} else {
			defer func() {
				if cerr := gz.Close(); cerr != nil {
					fmt.Printf("gnucash: close gzip: %v\n", cerr)
				}
			}()
			xmlReader = gz
		}
	} else {
		gz, err := gzip.NewReader(r)
		if err != nil {
			return nil, fmt.Errorf("gzip decompress: %w", err)
		}
		defer func() {
			if cerr := gz.Close(); cerr != nil {
				fmt.Printf("gnucash: close gzip: %v\n", cerr)
			}
		}()
		xmlReader = gz
	}

	return parseXML(xmlReader)
}

// --- XML element types matching the GnuCash namespace ---

// GnuCash uses XML namespaces extensively. Only the transaction
// namespace is needed:
//   gnc:  http://www.gnucash.org/XML/gnc
//   trn:  http://www.gnucash.org/XML/trn

const nsGNC = "http://www.gnucash.org/XML/gnc"

type xmlTransaction struct {
	Description string `xml:"http://www.gnucash.org/XML/trn description"`
}

func parseXML(r io.Reader) (*File, error) {
	dec := xml.NewDecoder(r)

	seen := make(map[string]bool)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read XML token: %w", err)
		}

		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}

		if se.Name.Space != nsGNC || se.Name.Local != "transaction" {
			// Skip unrelated elements without decoding their children.
			continue
		}

		var xt xmlTransaction
		if err := dec.DecodeElement(&xt, &se); err != nil {
			return nil, fmt.Errorf("decode transaction: %w", err)
		}
		if xt.Description != "" {
			seen[xt.Description] = true
		}
	}

	payees := make([]string, 0, len(seen))
	for desc := range seen {
		payees = append(payees, desc)
	}

	return &File{Payees: payees}, nil
}
