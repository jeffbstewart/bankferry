package gnucash

import (
	"bytes"
	"compress/gzip"
	"sort"
	"strings"
	"testing"
)

// sampleGnuCashXML is a minimal valid GnuCash XML document with
// accounts and transactions for testing. The accounts are present so
// the parser is exercised against realistic input, but they are not
// extracted.
const sampleGnuCashXML = `<?xml version="1.0" encoding="utf-8" ?>
<gnc-v2
     xmlns:gnc="http://www.gnucash.org/XML/gnc"
     xmlns:act="http://www.gnucash.org/XML/act"
     xmlns:book="http://www.gnucash.org/XML/book"
     xmlns:cd="http://www.gnucash.org/XML/cd"
     xmlns:cmdty="http://www.gnucash.org/XML/cmdty"
     xmlns:price="http://www.gnucash.org/XML/price"
     xmlns:slot="http://www.gnucash.org/XML/slot"
     xmlns:split="http://www.gnucash.org/XML/split"
     xmlns:trn="http://www.gnucash.org/XML/trn"
     xmlns:ts="http://www.gnucash.org/XML/ts">
<gnc:book version="2.0.0">
<book:id type="guid">book001</book:id>

<gnc:account version="2.0.0">
  <act:name>Root Account</act:name>
  <act:id type="guid">root001</act:id>
  <act:type>ROOT</act:type>
</gnc:account>

<gnc:account version="2.0.0">
  <act:name>Credit Card</act:name>
  <act:id type="guid">cc001</act:id>
  <act:type>CREDIT</act:type>
  <act:parent type="guid">root001</act:parent>
</gnc:account>

<gnc:account version="2.0.0">
  <act:name>Groceries</act:name>
  <act:id type="guid">exp002</act:id>
  <act:type>EXPENSE</act:type>
  <act:parent type="guid">root001</act:parent>
</gnc:account>

<gnc:account version="2.0.0">
  <act:name>Dining</act:name>
  <act:id type="guid">exp003</act:id>
  <act:type>EXPENSE</act:type>
  <act:parent type="guid">root001</act:parent>
</gnc:account>

<gnc:transaction version="2.0.0">
  <trn:description>Whole Foods</trn:description>
  <trn:splits>
    <trn:split>
      <split:account type="guid">cc001</split:account>
      <split:value>-5000/100</split:value>
    </trn:split>
    <trn:split>
      <split:account type="guid">exp002</split:account>
      <split:value>5000/100</split:value>
    </trn:split>
  </trn:splits>
</gnc:transaction>

<gnc:transaction version="2.0.0">
  <trn:description>Whole Foods</trn:description>
  <trn:splits>
    <trn:split>
      <split:account type="guid">cc001</split:account>
      <split:value>-3500/100</split:value>
    </trn:split>
    <trn:split>
      <split:account type="guid">exp002</split:account>
      <split:value>3500/100</split:value>
    </trn:split>
  </trn:splits>
</gnc:transaction>

<gnc:transaction version="2.0.0">
  <trn:description>Taco Bell</trn:description>
  <trn:splits>
    <trn:split>
      <split:account type="guid">cc001</split:account>
      <split:value>-1200/100</split:value>
    </trn:split>
    <trn:split>
      <split:account type="guid">exp003</split:account>
      <split:value>1200/100</split:value>
    </trn:split>
  </trn:splits>
</gnc:transaction>

<gnc:transaction version="2.0.0">
  <trn:description></trn:description>
  <trn:splits>
    <trn:split>
      <split:account type="guid">cc001</split:account>
      <split:value>-100/100</split:value>
    </trn:split>
  </trn:splits>
</gnc:transaction>

</gnc:book>
</gnc-v2>`

func gzipBytes(data string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(data)); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestParseReader_Gzipped(t *testing.T) {
	gz := gzipBytes(sampleGnuCashXML)
	f, err := ParseReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	checkParsedFile(t, f)
}

func TestParseReader_PlainXML(t *testing.T) {
	// ReadSeeker allows fallback to plain XML.
	f, err := ParseReader(strings.NewReader(sampleGnuCashXML))
	if err != nil {
		t.Fatalf("ParseReader: %v", err)
	}
	checkParsedFile(t, f)
}

func checkParsedFile(t *testing.T, f *File) {
	t.Helper()

	// "Whole Foods" is deduplicated from two transactions, "Taco Bell"
	// appears once, and the empty description is skipped.
	got := append([]string(nil), f.Payees...)
	sort.Strings(got)

	want := []string{"Taco Bell", "Whole Foods"}
	if len(got) != len(want) {
		t.Fatalf("payees = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("payees = %v, want %v", got, want)
			break
		}
	}
}

func TestParseReader_InvalidGzip(t *testing.T) {
	// A *bytes.Buffer is an io.Reader but not an io.Seeker, so it exercises the
	// non-seekable path with non-gzip data, which must fail.
	_, err := ParseReader(bytes.NewBufferString("not gzip or xml"))
	if err == nil {
		t.Error("expected error for non-gzip non-seekable reader")
	}
}
