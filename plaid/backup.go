package plaid

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/argon2"
)

// Backups exist because a Plaid access token cannot be recovered. It lives
// only in this machine's keyring, Plaid never reissues it, and /item/remove
// needs it, so a lost token strands an Item that can be neither read nor
// deleted. Re-linking creates a duplicate Item and permanently consumes one
// of the ten a Trial account is allowed. See Plaid.md section 6.2.
//
// The backup therefore holds the one irreplaceable thing and nothing else.
// The client ID and API secret are deliberately excluded: both can be
// downloaded again from the Plaid dashboard, so including them would widen
// the blast radius of a leaked file for no benefit.
//
// An access token grants read-only transaction access and can be revoked
// through Plaid Portal without possessing it. That is not a reason to be
// careless, but it does bound the damage.

const (
	backupMagic       = "TAPLDBK1"
	backupFormat      = 1
	kdfArgon2id  byte = 1

	// Argon2id parameters. 64 MiB and three passes is the low end of the
	// RFC 9106 second recommended option, comfortable on a desktop and
	// expensive to attack in bulk.
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32

	saltLen  = 16
	nonceLen = 12
)

var (
	// ErrBackupFormat reports a file this build cannot read.
	ErrBackupFormat = errors.New("plaid: unrecognized backup file")
	// ErrBackupDecrypt reports a wrong passphrase or a tampered file. The
	// two are indistinguishable by design: AES-GCM authenticates the whole
	// header and ciphertext together.
	ErrBackupDecrypt = errors.New("plaid: could not decrypt the backup; " +
		"the passphrase is wrong or the file has been altered")
)

// BackupFile is the decrypted contents of an export.
type BackupFile struct {
	Format      int         `json:"format"`
	Environment Environment `json:"environment"`
	ExportedAt  time.Time   `json:"exported_at"`
	Items       []Item      `json:"items"`
}

// header is everything before the ciphertext. It is authenticated but not
// encrypted, so the environment and KDF parameters can be read before a
// passphrase is offered, and cannot be altered without failing decryption.
type header struct {
	format      byte
	kdf         byte
	time        uint32
	memory      uint32
	threads     byte
	salt        []byte
	environment Environment
	nonce       []byte
}

func (h header) encode() ([]byte, error) {
	if len(h.environment) > 255 {
		return nil, fmt.Errorf("plaid: environment name is too long")
	}

	var b bytes.Buffer
	b.WriteString(backupMagic)
	b.WriteByte(h.format)
	b.WriteByte(h.kdf)
	if err := binary.Write(&b, binary.BigEndian, h.time); err != nil {
		return nil, err
	}
	if err := binary.Write(&b, binary.BigEndian, h.memory); err != nil {
		return nil, err
	}
	b.WriteByte(h.threads)
	b.WriteByte(byte(len(h.salt)))
	b.Write(h.salt)
	b.WriteByte(byte(len(h.environment)))
	b.WriteString(string(h.environment))
	b.Write(h.nonce)
	return b.Bytes(), nil
}

// decodeHeader reads the header and returns it with the remaining bytes.
func decodeHeader(blob []byte) (header, []byte, error) {
	r := bytes.NewReader(blob)

	magic := make([]byte, len(backupMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return header{}, nil, fmt.Errorf("%w: too short", ErrBackupFormat)
	}
	if string(magic) != backupMagic {
		return header{}, nil, fmt.Errorf("%w: bad magic", ErrBackupFormat)
	}

	var h header
	var err error
	if h.format, err = r.ReadByte(); err != nil {
		return header{}, nil, fmt.Errorf("%w: truncated", ErrBackupFormat)
	}
	if h.format != backupFormat {
		return header{}, nil, fmt.Errorf("%w: format %d, this build reads %d",
			ErrBackupFormat, h.format, backupFormat)
	}
	if h.kdf, err = r.ReadByte(); err != nil {
		return header{}, nil, fmt.Errorf("%w: truncated", ErrBackupFormat)
	}
	if h.kdf != kdfArgon2id {
		return header{}, nil, fmt.Errorf("%w: unknown key derivation %d", ErrBackupFormat, h.kdf)
	}
	if err = binary.Read(r, binary.BigEndian, &h.time); err != nil {
		return header{}, nil, fmt.Errorf("%w: truncated", ErrBackupFormat)
	}
	if err = binary.Read(r, binary.BigEndian, &h.memory); err != nil {
		return header{}, nil, fmt.Errorf("%w: truncated", ErrBackupFormat)
	}
	if h.threads, err = r.ReadByte(); err != nil {
		return header{}, nil, fmt.Errorf("%w: truncated", ErrBackupFormat)
	}

	h.salt, err = readLengthPrefixed(r)
	if err != nil {
		return header{}, nil, err
	}
	env, err := readLengthPrefixed(r)
	if err != nil {
		return header{}, nil, err
	}
	h.environment = Environment(env)

	h.nonce = make([]byte, nonceLen)
	if _, err := io.ReadFull(r, h.nonce); err != nil {
		return header{}, nil, fmt.Errorf("%w: truncated nonce", ErrBackupFormat)
	}

	consumed := len(blob) - r.Len()
	return h, blob[consumed:], nil
}

func readLengthPrefixed(r *bytes.Reader) ([]byte, error) {
	n, err := r.ReadByte()
	if err != nil {
		return nil, fmt.Errorf("%w: truncated", ErrBackupFormat)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("%w: truncated", ErrBackupFormat)
	}
	return buf, nil
}

func deriveKey(passphrase, salt []byte, h header) []byte {
	return argon2.IDKey(passphrase, salt, h.time, h.memory, h.threads, argonKeyLen)
}

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("plaid: building cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("plaid: building GCM: %w", err)
	}
	return gcm, nil
}

// EncryptItems serializes the Items and encrypts them under a passphrase.
//
// The header is authenticated as additional data, so the environment cannot
// be swapped: a sandbox export can never be decrypted as a production one.
func EncryptItems(env Environment, items []Item, passphrase []byte) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errors.New("plaid: passphrase is empty")
	}
	if env == "" {
		return nil, errors.New("plaid: environment is required")
	}

	payload, err := json.Marshal(BackupFile{
		Format:      backupFormat,
		Environment: env,
		ExportedAt:  time.Now().UTC(),
		Items:       items,
	})
	if err != nil {
		return nil, fmt.Errorf("plaid: encoding backup: %w", err)
	}

	h := header{
		format:      backupFormat,
		kdf:         kdfArgon2id,
		time:        argonTime,
		memory:      argonMemory,
		threads:     argonThreads,
		salt:        make([]byte, saltLen),
		environment: env,
		nonce:       make([]byte, nonceLen),
	}
	if _, err := rand.Read(h.salt); err != nil {
		return nil, fmt.Errorf("plaid: generating salt: %w", err)
	}
	if _, err := rand.Read(h.nonce); err != nil {
		return nil, fmt.Errorf("plaid: generating nonce: %w", err)
	}

	encoded, err := h.encode()
	if err != nil {
		return nil, err
	}

	key := deriveKey(passphrase, h.salt, h)
	defer zero(key)

	gcm, err := aead(key)
	if err != nil {
		return nil, err
	}

	ciphertext := gcm.Seal(nil, h.nonce, payload, encoded)
	zero(payload)

	return append(encoded, ciphertext...), nil
}

// DecryptItems reads an export. A wrong passphrase and a tampered file are
// reported identically, because AES-GCM cannot tell them apart and neither
// should the caller.
func DecryptItems(blob, passphrase []byte) (BackupFile, error) {
	if len(passphrase) == 0 {
		return BackupFile{}, errors.New("plaid: passphrase is empty")
	}

	h, ciphertext, err := decodeHeader(blob)
	if err != nil {
		return BackupFile{}, err
	}

	encoded, err := h.encode()
	if err != nil {
		return BackupFile{}, err
	}

	key := deriveKey(passphrase, h.salt, h)
	defer zero(key)

	gcm, err := aead(key)
	if err != nil {
		return BackupFile{}, err
	}

	payload, err := gcm.Open(nil, h.nonce, ciphertext, encoded)
	if err != nil {
		return BackupFile{}, ErrBackupDecrypt
	}
	defer zero(payload)

	var file BackupFile
	if err := json.Unmarshal(payload, &file); err != nil {
		return BackupFile{}, fmt.Errorf("plaid: decrypted backup is not valid JSON: %w", err)
	}
	if file.Environment != h.environment {
		return BackupFile{}, fmt.Errorf(
			"plaid: backup header says %s but its contents say %s",
			h.environment, file.Environment)
	}
	return file, nil
}

// BackupEnvironment reports which environment a file holds, without a
// passphrase. The header is authenticated, so a lie here fails decryption.
func BackupEnvironment(blob []byte) (Environment, error) {
	h, _, err := decodeHeader(blob)
	if err != nil {
		return "", err
	}
	return h.environment, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
