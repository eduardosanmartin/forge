package approval

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Sentinel typed errors for Verify.
var (
	ErrBadVersion   = errors.New("approval: bad version")
	ErrAlgMismatch  = errors.New("approval: algorithm mismatch")
	ErrHashMismatch = errors.New("approval: hash mismatch")
	ErrBadSignature = errors.New("approval: bad signature")
	ErrUntrustedKey = errors.New("approval: untrusted key")
	ErrBadRecord    = errors.New("approval: bad record")
)

// Record is the v2 signed approval record.
// sig is computed over canonical JSON of all fields except sig.
type Record struct {
	V        int    `json:"v"`
	Alg      string `json:"alg"`
	Artifact string `json:"artifact"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	TS       int64  `json:"ts"`
	Pub      string `json:"pub"`
	Sig      string `json:"sig"`
}

// canonical is the struct marshaled for signing (excludes Sig).
// Field order is the canonical order.
type canonical struct {
	V        int    `json:"v"`
	Alg      string `json:"alg"`
	Artifact string `json:"artifact"`
	Name     string `json:"name"`
	SHA256   string `json:"sha256"`
	TS       int64  `json:"ts"`
	Pub      string `json:"pub"`
}

// canonicalBytes returns the JSON bytes that are signed.
func canonicalBytes(rec Record) ([]byte, error) {
	c := canonical{
		V:        rec.V,
		Alg:      rec.Alg,
		Artifact: rec.Artifact,
		Name:     rec.Name,
		SHA256:   rec.SHA256,
		TS:       rec.TS,
		Pub:      rec.Pub,
	}
	return json.Marshal(c)
}

// Sign creates a v2 Record signed by priv over the canonical serialization.
// artifact must be "plugin" or "skill", name non-empty, sha256 must be
// "sha256:<64hex>", ts is unix seconds (if 0, current time is used by caller).
func Sign(priv ed25519.PrivateKey, artifact, name, sha256Hex string, ts int64) (Record, error) {
	if artifact != "plugin" && artifact != "skill" {
		return Record{}, fmt.Errorf("%w: artifact must be plugin or skill, got %q", ErrBadRecord, artifact)
	}
	if strings.TrimSpace(name) == "" {
		return Record{}, fmt.Errorf("%w: name must not be empty", ErrBadRecord)
	}
	if !strings.HasPrefix(sha256Hex, "sha256:") {
		return Record{}, fmt.Errorf("%w: sha256 must have sha256: prefix, got %q", ErrBadRecord, sha256Hex)
	}
	hexPart := strings.TrimPrefix(sha256Hex, "sha256:")
	if len(hexPart) != 64 {
		return Record{}, fmt.Errorf("%w: sha256 hex must be 64 chars", ErrBadRecord)
	}
	if len(priv) != ed25519.PrivateKeySize {
		return Record{}, fmt.Errorf("%w: private key size must be %d", ErrBadRecord, ed25519.PrivateKeySize)
	}
	pub := priv.Public().(ed25519.PublicKey)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	rec := Record{
		V:        2,
		Alg:      "ed25519",
		Artifact: artifact,
		Name:     name,
		SHA256:   sha256Hex,
		TS:       ts,
		Pub:      pubB64,
	}
	canonical, err := canonicalBytes(rec)
	if err != nil {
		return Record{}, err
	}
	sig := ed25519.Sign(priv, canonical)
	rec.Sig = base64.StdEncoding.EncodeToString(sig)
	return rec, nil
}

// Verify checks a v2 record against the expected hash and anchor public key.
// expectedSHA256 must be "sha256:<hex>" of the current artifact bytes.
// anchorPub is the trust anchor from user config; if nil, trust is not
// established but Verify still validates signature and hash (caller decides
// fallback policy). When anchorPub is non-nil, embedded pub must match it
// else ErrUntrustedKey.
func Verify(rec Record, expectedSHA256 string, anchorPub ed25519.PublicKey) error {
	if rec.V != 2 {
		return fmt.Errorf("%w: got %d want 2", ErrBadVersion, rec.V)
	}
	if rec.Alg != "ed25519" {
		return fmt.Errorf("%w: got %q want ed25519", ErrAlgMismatch, rec.Alg)
	}
	if rec.Artifact != "plugin" && rec.Artifact != "skill" {
		return fmt.Errorf("%w: unknown artifact %q", ErrBadVersion, rec.Artifact)
	}
	if strings.TrimSpace(rec.Name) == "" {
		return fmt.Errorf("%w: empty name", ErrBadRecord)
	}
	if strings.TrimSpace(rec.SHA256) == "" {
		return fmt.Errorf("%w: empty sha256", ErrBadRecord)
	}
	if !strings.HasPrefix(rec.SHA256, "sha256:") {
		return fmt.Errorf("%w: sha256 missing prefix", ErrBadRecord)
	}
	// Hash must match expected artifact hash.
	if !strings.EqualFold(strings.TrimSpace(rec.SHA256), strings.TrimSpace(expectedSHA256)) {
		return fmt.Errorf("%w: record %q vs expected %q", ErrHashMismatch, rec.SHA256, expectedSHA256)
	}
	// Decode embedded pub.
	pubBytes, err := base64.StdEncoding.DecodeString(rec.Pub)
	if err != nil {
		return fmt.Errorf("%w: bad pub encoding: %v", ErrBadRecord, err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: pub size %d", ErrBadRecord, len(pubBytes))
	}
	// Anchor check: if anchor provided, embedded pub must equal anchor.
	if anchorPub != nil {
		if len(anchorPub) != ed25519.PublicKeySize {
			return fmt.Errorf("%w: anchor size invalid", ErrBadRecord)
		}
		if !equalBytes(pubBytes, []byte(anchorPub)) {
			return fmt.Errorf("%w: record pub does not match anchor", ErrUntrustedKey)
		}
	}
	// Decode and verify signature over canonical bytes.
	sigBytes, err := base64.StdEncoding.DecodeString(rec.Sig)
	if err != nil {
		return fmt.Errorf("%w: bad sig encoding: %v", ErrBadSignature, err)
	}
	canonical, err := canonicalBytes(rec)
	if err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(pubBytes), canonical, sigBytes) {
		return ErrBadSignature
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
