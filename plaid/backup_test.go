package plaid

import (
	"bytes"
	"errors"
	"testing"
)

func backupItems() []Item {
	return []Item{
		{Version: itemSchemaVersion, ItemID: "item_1", AccessToken: "access-sandbox-aaa",
			InstitutionID: "ins_1", InstitutionName: "Bank of America"},
		{Version: itemSchemaVersion, ItemID: "item_2", AccessToken: "access-sandbox-bbb",
			InstitutionID: "ins_2", InstitutionName: "Capital One"},
	}
}

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

func TestEncryptDecryptItems_RoundTrip(t *testing.T) {
	pass := []byte("correct horse battery staple")

	blob, err := EncryptItems(Sandbox, backupItems(), pass)
	if err != nil {
		t.Fatalf("EncryptItems: %v", err)
	}

	got, err := DecryptItems(blob, pass)
	if err != nil {
		t.Fatalf("DecryptItems: %v", err)
	}

	if got.Environment != Sandbox {
		t.Errorf("environment = %q", got.Environment)
	}
	if got.Format != backupFormat {
		t.Errorf("format = %d", got.Format)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	for i, want := range backupItems() {
		if got.Items[i] != want {
			t.Errorf("item %d = %+v, want %+v", i, got.Items[i], want)
		}
	}
	if got.ExportedAt.IsZero() {
		t.Error("exported_at was not recorded")
	}
}

// The ciphertext must not contain the plaintext token, which would defeat
// the whole exercise.
func TestEncryptItems_TokenIsNotInTheClear(t *testing.T) {
	blob, err := EncryptItems(Sandbox, backupItems(), []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("access-sandbox-aaa")) {
		t.Fatal("the access token appears in the encrypted file")
	}
	if bytes.Contains(blob, []byte("Capital One")) {
		t.Error("institution names should be encrypted too")
	}
}

// Two exports of the same data must differ: fresh salt, fresh nonce.
func TestEncryptItems_IsNotDeterministic(t *testing.T) {
	pass := []byte("pw")
	a, err := EncryptItems(Sandbox, backupItems(), pass)
	if err != nil {
		t.Fatal(err)
	}
	b, err := EncryptItems(Sandbox, backupItems(), pass)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two exports are byte-identical; the salt or nonce is reused")
	}
}

func TestEncryptItems_RejectsEmptyInputs(t *testing.T) {
	if _, err := EncryptItems(Sandbox, backupItems(), nil); err == nil {
		t.Error("expected an error for an empty passphrase")
	}
	if _, err := EncryptItems("", backupItems(), []byte("pw")); err == nil {
		t.Error("expected an error for an empty environment")
	}
}

// An export with no Items is still a valid file. It says, truthfully, that
// nothing was linked.
func TestEncryptItems_EmptyItemList(t *testing.T) {
	blob, err := EncryptItems(Sandbox, nil, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecryptItems(blob, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 {
		t.Errorf("items = %d, want 0", len(got.Items))
	}
}

// ---------------------------------------------------------------------------
// Failure modes
// ---------------------------------------------------------------------------

func TestDecryptItems_WrongPassphrase(t *testing.T) {
	blob, err := EncryptItems(Sandbox, backupItems(), []byte("right"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptItems(blob, []byte("wrong")); !errors.Is(err, ErrBackupDecrypt) {
		t.Errorf("err = %v, want ErrBackupDecrypt", err)
	}
}

// Every byte is authenticated. Flipping any one of them must fail.
func TestDecryptItems_TamperedCiphertext(t *testing.T) {
	pass := []byte("pw")
	blob, err := EncryptItems(Sandbox, backupItems(), pass)
	if err != nil {
		t.Fatal(err)
	}

	tampered := append([]byte(nil), blob...)
	tampered[len(tampered)-1] ^= 0x01

	if _, err := DecryptItems(tampered, pass); !errors.Is(err, ErrBackupDecrypt) {
		t.Errorf("err = %v, want ErrBackupDecrypt", err)
	}
}

// A corrupt or malicious header must fail cleanly. The KDF parameters are read
// before authentication (the derived key is needed to open the seal), and
// argon2 panics on a zero time or thread count and would allocate a tampered
// memory field — terabytes in the worst case — so decodeHeader bounds them.
// This is the class of corruption verify exists to report, so it must never
// panic or OOM.
func TestDecryptItems_OutOfRangeKDFParams(t *testing.T) {
	const (
		timeOff    = len(backupMagic) + 2 // past magic, format, kdf
		memoryOff  = timeOff + 4          // time is a big-endian uint32
		threadsOff = memoryOff + 4        // memory is a big-endian uint32
	)
	cases := []struct {
		name    string
		corrupt func([]byte)
	}{
		{"zero time", func(b []byte) { b[timeOff], b[timeOff+1], b[timeOff+2], b[timeOff+3] = 0, 0, 0, 0 }},
		{"zero threads", func(b []byte) { b[threadsOff] = 0 }},
		{"oversize memory", func(b []byte) { b[memoryOff], b[memoryOff+1], b[memoryOff+2], b[memoryOff+3] = 0xFF, 0xFF, 0xFF, 0xFF }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob, err := EncryptItems(Sandbox, backupItems(), []byte("pw"))
			if err != nil {
				t.Fatalf("EncryptItems: %v", err)
			}
			tc.corrupt(blob)
			// Must return an error — and, crucially, not panic or OOM.
			if _, err := DecryptItems(blob, []byte("pw")); !errors.Is(err, ErrBackupFormat) {
				t.Errorf("err = %v, want ErrBackupFormat", err)
			}
		})
	}
}

// The header is authenticated as additional data, so rewriting the salt,
// the nonce, or the environment must also fail. A sandbox export must never
// be decryptable as a production one.
func TestDecryptItems_TamperedHeaderFails(t *testing.T) {
	pass := []byte("pw")
	blob, err := EncryptItems(Sandbox, backupItems(), pass)
	if err != nil {
		t.Fatal(err)
	}

	// The salt begins after magic(8) + format(1) + kdf(1) + time(4) +
	// memory(4) + threads(1) + saltLen(1).
	const saltOffset = 8 + 1 + 1 + 4 + 4 + 1 + 1

	for _, offset := range []int{saltOffset, saltOffset + 5, len(blob) - 30} {
		tampered := append([]byte(nil), blob...)
		tampered[offset] ^= 0x01
		if _, err := DecryptItems(tampered, pass); err == nil {
			t.Errorf("altering byte %d was accepted", offset)
		}
	}
}

// Swapping the environment label in the header must not decrypt. It is
// covered by the AEAD's additional data.
func TestDecryptItems_EnvironmentIsAuthenticated(t *testing.T) {
	pass := []byte("pw")
	blob, err := EncryptItems(Sandbox, backupItems(), pass)
	if err != nil {
		t.Fatal(err)
	}

	i := bytes.Index(blob, []byte("sandbox"))
	if i < 0 {
		t.Fatal("environment label not found in the header")
	}
	tampered := append([]byte(nil), blob...)
	copy(tampered[i:], []byte("sandboX"))

	if _, err := DecryptItems(tampered, pass); !errors.Is(err, ErrBackupDecrypt) {
		t.Errorf("err = %v, want ErrBackupDecrypt", err)
	}
}

func TestDecryptItems_BadMagicAndShortFile(t *testing.T) {
	if _, err := DecryptItems([]byte("nope"), []byte("pw")); !errors.Is(err, ErrBackupFormat) {
		t.Errorf("short file: err = %v, want ErrBackupFormat", err)
	}
	if _, err := DecryptItems(bytes.Repeat([]byte("x"), 64), []byte("pw")); !errors.Is(err, ErrBackupFormat) {
		t.Errorf("bad magic: err = %v, want ErrBackupFormat", err)
	}
}

// A file written by a future build must be refused clearly, not parsed as
// though its layout were understood.
func TestDecryptItems_FutureFormatIsRefused(t *testing.T) {
	blob, err := EncryptItems(Sandbox, backupItems(), []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	blob[len(backupMagic)] = backupFormat + 1

	_, err = DecryptItems(blob, []byte("pw"))
	if !errors.Is(err, ErrBackupFormat) {
		t.Errorf("err = %v, want ErrBackupFormat", err)
	}
}

func TestDecryptItems_EmptyPassphrase(t *testing.T) {
	blob, err := EncryptItems(Sandbox, backupItems(), []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptItems(blob, nil); err == nil {
		t.Error("expected an error for an empty passphrase")
	}
}

// ---------------------------------------------------------------------------
// Header inspection without a passphrase
// ---------------------------------------------------------------------------

func TestBackupEnvironment(t *testing.T) {
	blob, err := EncryptItems(Sandbox, backupItems(), []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := BackupEnvironment(blob)
	if err != nil {
		t.Fatalf("BackupEnvironment: %v", err)
	}
	if env != Sandbox {
		t.Errorf("environment = %q, want sandbox", env)
	}
}

// Sandbox and production exports are distinguishable before decryption, and
// the label cannot be forged, since it is authenticated.
func TestBackupEnvironment_DistinguishesEnvironments(t *testing.T) {
	pass := []byte("pw")
	s, err := EncryptItems(Sandbox, backupItems(), pass)
	if err != nil {
		t.Fatal(err)
	}
	p, err := EncryptItems(Production, backupItems(), pass)
	if err != nil {
		t.Fatal(err)
	}

	se, err := BackupEnvironment(s)
	if err != nil {
		t.Fatal(err)
	}
	pe, err := BackupEnvironment(p)
	if err != nil {
		t.Fatal(err)
	}
	if se == pe {
		t.Fatal("sandbox and production exports are indistinguishable")
	}
}
