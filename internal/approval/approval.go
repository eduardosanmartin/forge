package approval

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// IsV2 reports whether data is a v2 JSON record (first non-space byte is '{').
func IsV2(data []byte) bool {
	for _, b := range data {
		if b == ' ' || b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		return b == '{'
	}
	return false
}

// ParseV2 parses a v2 JSON record from data.
func ParseV2(data []byte) (Record, error) {
	var rec Record
	trimmed := strings.TrimSpace(string(data))
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrBadRecord, err)
	}
	return rec, nil
}

// VerifyFile verifies approved.flag in dir against expectedSHA256 for the
// given artifact and name. It handles v1 and v2 formats, anchor fallback,
// and logs appropriate warnings via logger (or slog.Default if nil).
// artifact must be "plugin" or "skill".
// Returns nil if approved, otherwise a typed error (ErrHashMismatch,
// ErrBadSignature, ErrUntrustedKey, etc.) that caller should wrap into
// ErrApprovalRequired.
func VerifyFile(dir, artifact, name, expectedSHA256 string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	flagPath := filepath.Join(dir, "approved.flag")
	data, err := os.ReadFile(flagPath)
	if err != nil {
		return fmt.Errorf("%w: approved.flag missing: %v", ErrHashMismatch, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return fmt.Errorf("%w: empty approved.flag", ErrBadRecord)
	}

	if IsV2(data) {
		rec, err := ParseV2(data)
		if err != nil {
			return err
		}
		// Basic artifact/name consistency: if record's name/artifact differ
		// from expected, treat as hash mismatch (record was for another artifact).
		if rec.Artifact != artifact {
			return fmt.Errorf("%w: artifact mismatch record %q vs expected %q", ErrHashMismatch, rec.Artifact, artifact)
		}
		if rec.Name != name {
			return fmt.Errorf("%w: name mismatch record %q vs expected %q", ErrHashMismatch, rec.Name, name)
		}
		anchor, hasAnchor, err := LoadAnchor()
		if err != nil {
			// Anchor EXISTS but is unreadable/corrupt — fail closed. Falling
			// back to the hash check here would let a tampered anchor file
			// silently downgrade every approval to unsigned trust.
			return fmt.Errorf("%w: anchor present but unusable: %v", ErrUntrustedKey, err)
		}
		if !hasAnchor {
			logger.Warn("v2 record without anchor — falling back to hash check", "dir", dir)
			if strings.EqualFold(strings.TrimSpace(rec.SHA256), strings.TrimSpace(expectedSHA256)) {
				return nil
			}
			return fmt.Errorf("%w: v2 hash mismatch without anchor", ErrHashMismatch)
		}
		// Anchor present: full verify.
		if err := Verify(rec, expectedSHA256, anchor); err != nil {
			// Log specific reason
			logger.Warn("v2 approval verification failed", "error", err, "dir", dir)
			return err
		}
		return nil
	}
	// v1 path
	logger.Warn("v1 approval record deprecated — re-approve to generate v2 signed record", "dir", dir)
	if !strings.HasPrefix(trimmed, "sha256:") {
		return fmt.Errorf("%w: invalid v1 record %q", ErrBadRecord, trimmed)
	}
	if strings.EqualFold(trimmed, strings.TrimSpace(expectedSHA256)) {
		return nil
	}
	return fmt.Errorf("%w: v1 hash mismatch %q vs %q", ErrHashMismatch, trimmed, expectedSHA256)
}

// WriteV2 writes a v2 signed record to dir/approved.flag for the given
// artifact/name/hash. It requires a keypair (private key loaded from
// default location). ts is unix seconds; if 0, current time is used via time.Now (caller ensures).
func WriteV2(dir, artifact, name, sha256Hex string, priv ed25519.PrivateKey, ts int64) error {
	rec, err := Sign(priv, artifact, name, sha256Hex, ts)
	if err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	// Ensure dir exists
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	flagPath := filepath.Join(dir, "approved.flag")
	return os.WriteFile(flagPath, append(data, '\n'), 0o644)
}

// ExtractSHA256 extracts the sha256 field from approved.flag if present,
// handling both v1 and v2. Returns empty if unreadable.
func ExtractSHA256(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "approved.flag"))
	if err != nil {
		return ""
	}
	if IsV2(data) {
		rec, err := ParseV2(data)
		if err == nil {
			return rec.SHA256
		}
		return ""
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "sha256:") {
		return trimmed
	}
	return ""
}
