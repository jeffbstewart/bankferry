package plaid

// A fake security key, and a synthetic vendor PKI for it to attest to.
//
// touchvault owns the cryptography now, and tests it against a fake of its
// own. This one exists for a narrower job: to reach the glue in
// hardwarekey.go — which vault an environment uses, what the secret is called
// inside it, the slot policy — none of which can be exercised without
// something that can be enrolled.
//
// fido.New refuses to open under a test binary, so a real key can never reach
// these tests by accident. The fake must therefore be faithful in the ways the
// glue depends on: the derivation is per-credential, and it depends on the
// salt (touchvault's enrollment gate proves the latter and refuses to seal
// anything without it, so a fake that ignored the salt could not enroll at
// all).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/jeffbstewart/touchvault"
)

// coseES256 is the COSE algorithm identifier for ECDSA-P256-SHA256, the only
// algorithm touchvault's attestation verifier accepts.
const coseES256 = -7

// oidFidoGenCeAAGUID carries the authenticator's AAGUID in its attestation
// certificate. touchvault cross-checks it against the authenticator data.
var oidFidoGenCeAAGUID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 45724, 1, 1, 4}

func testAAGUID() []byte {
	return []byte{9, 9, 9, 9, 8, 8, 8, 8, 7, 7, 7, 7, 6, 6, 6, 6}
}

// testAuthData builds authenticator data carrying an AAGUID where the verifier
// reads it: rpIdHash(32) flags(1) signCount(4) aaguid(16).
func testAuthData() []byte {
	data := make([]byte, 53)
	copy(data[37:53], testAAGUID())
	return data
}

// --- the synthetic vendor -------------------------------------------------

// testPKI is a vendor chain — root -> intermediate -> device leaf — plus the
// device key that signs attestations. It stands in for Yubico's, which no test
// can produce a key for.
type testPKI struct {
	leafDER  []byte
	interDER []byte
	leafKey  *ecdsa.PrivateKey
	roots    *x509.CertPool
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()

	rootKey := mustECDSAKey(t)
	rootTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Vendor FIDO Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER := mustCreateCert(t, rootTmpl, rootTmpl, &rootKey.PublicKey, rootKey)

	interKey := mustECDSAKey(t)
	interTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "Test Vendor Attestation CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	interDER := mustCreateCert(t, interTmpl, mustParseCert(t, rootDER), &interKey.PublicKey, rootKey)

	aaguidValue, err := asn1.Marshal(testAAGUID())
	if err != nil {
		t.Fatalf("asn1.Marshal(aaguid) = %v", err)
	}

	leafKey := mustECDSAKey(t)
	leafTmpl := &x509.Certificate{
		SerialNumber:    big.NewInt(3),
		Subject:         pkix.Name{CommonName: "Test Vendor Authenticator"},
		NotBefore:       time.Now().Add(-time.Hour),
		NotAfter:        time.Now().Add(24 * time.Hour),
		ExtraExtensions: []pkix.Extension{{Id: oidFidoGenCeAAGUID, Value: aaguidValue}},
	}
	leafDER := mustCreateCert(t, leafTmpl, mustParseCert(t, interDER), &leafKey.PublicKey, interKey)

	roots := x509.NewCertPool()
	roots.AddCert(mustParseCert(t, rootDER))

	return &testPKI{leafDER: leafDER, interDER: interDER, leafKey: leafKey, roots: roots}
}

// attest builds the material a genuine device returns: a packed statement
// signed by the device's leaf key over authData || clientDataHash.
func (pki *testPKI) attest(t *testing.T) touchvault.EnrollResult {
	t.Helper()

	authData := testAuthData()
	clientDataHash := sha256.Sum256([]byte("fake-client-data"))

	signed := append(append([]byte(nil), authData...), clientDataHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, pki.leafKey, digest[:])
	if err != nil {
		t.Fatalf("ecdsa.SignASN1() = %v", err)
	}

	return touchvault.EnrollResult{
		AttestationFormat:    "packed",
		AttestationAlg:       coseES256,
		AttestationSignature: sig,
		AuthenticatorData:    authData,
		ClientDataHash:       clientDataHash[:],
		AttestationCerts:     [][]byte{pki.leafDER, pki.interDER},
	}
}

func mustECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() = %v", err)
	}
	return k
}

func mustCreateCert(t *testing.T, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatalf("x509.CreateCertificate() = %v", err)
	}
	return der
}

func mustParseCert(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("x509.ParseCertificate() = %v", err)
	}
	return c
}

// --- the fake key ---------------------------------------------------------

// fakeAuth is one or more security keys. present names which credentials the
// "operator" is currently holding, so a test can model swapping keys.
type fakeAuth struct {
	t *testing.T

	// pki attests the credentials this key creates. Nil means the key attests
	// to nothing, as a software authenticator does — which enrollment refuses.
	pki *testPKI

	// seeds maps a credential ID to its per-credential secret, which a real
	// authenticator keeps on the device and never reveals.
	seeds map[string][]byte

	// present, when non-empty, restricts which credentials this key will
	// answer for. Empty means it holds all of them.
	present map[string]bool

	enrollCalls int
	deriveCalls int
}

// newFakeKey returns a fake that attests as genuine hardware, together with
// the anchors that vouch for it. Install those with useTestRoots.
func newFakeKey(t *testing.T) (*fakeAuth, *x509.CertPool) {
	t.Helper()
	pki := newTestPKI(t)
	return &fakeAuth{t: t, pki: pki, seeds: map[string][]byte{}, present: map[string]bool{}}, pki.roots
}

// useTestRoots points the package's attestation policy at the synthetic
// vendor, and restores the real one when the test ends.
func useTestRoots(t *testing.T, roots *x509.CertPool) {
	t.Helper()
	previous := attestationRoots
	attestationRoots = func() *x509.CertPool { return roots }
	t.Cleanup(func() { attestationRoots = previous })
}

// hold makes the fake answer only for these credentials, modeling an operator
// who has unplugged one key and plugged in another.
func (f *fakeAuth) hold(credentialIDs ...[]byte) {
	f.present = map[string]bool{}
	for _, id := range credentialIDs {
		f.present[string(id)] = true
	}
}

func (f *fakeAuth) holds(credID []byte) bool {
	if _, known := f.seeds[string(credID)]; !known {
		return false
	}
	if len(f.present) == 0 {
		return true
	}
	return f.present[string(credID)]
}

func (f *fakeAuth) Enroll(req touchvault.EnrollRequest) (touchvault.EnrollResult, error) {
	f.enrollCalls++

	credID := []byte(fmt.Sprintf("fake-credential-%d", f.enrollCalls))
	f.seeds[string(credID)] = []byte(fmt.Sprintf("fake-seed-%d", f.enrollCalls))

	// A newly created credential is on the key that was just touched, so it is
	// held even when the test has narrowed `present` to model a key swap.
	if len(f.present) > 0 {
		f.present[string(credID)] = true
	}

	var result touchvault.EnrollResult
	if f.pki != nil {
		result = f.pki.attest(f.t)
	} else {
		// A software authenticator produces a well-formed statement and simply
		// certifies nothing: the format is real, the chain is empty. That is
		// the case worth testing — an empty format string would be refused for
		// being malformed, which proves nothing about the trust policy.
		result.AttestationFormat = "packed"
	}
	result.CredentialID = credID
	result.PRFEnabled = true
	return result, nil
}

func (f *fakeAuth) Derive(req touchvault.DeriveRequest) (touchvault.DeriveResult, error) {
	f.deriveCalls++

	// The port validates on the way in, and a fake that were more permissive
	// than the hardware it stands in for would let tests pass on inputs a real
	// key rejects.
	if err := req.Validate(); err != nil {
		return touchvault.DeriveResult{}, err
	}

	// The platform picks whichever credential in the allow-list is on a key the
	// operator actually presented.
	var credID []byte
	for _, id := range req.CredentialIDs {
		if f.holds(id) {
			credID = id
			break
		}
	}
	if credID == nil {
		return touchvault.DeriveResult{}, errors.New("fake: no credential in the allow-list is on a present key")
	}

	return touchvault.DeriveResult{
		Secret:       f.prf(credID, req.Salt),
		CredentialID: credID,
		UserPresent:  true,
	}, nil
}

// prf models hmac-secret: a function of the per-credential seed and the salt.
func (f *fakeAuth) prf(credID, salt []byte) []byte {
	mac := hmac.New(sha256.New, f.seeds[string(credID)])
	mac.Write(salt)
	return mac.Sum(nil)
}
