package approval

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// KeysDir returns the user-level keys directory.
// It respects FORGE_KEYS_DIR env var for tests; otherwise uses
// os.UserConfigDir()/forge/keys.
func KeysDir() (string, error) {
	if dir := os.Getenv("FORGE_KEYS_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config dir: %w", err)
	}
	return filepath.Join(base, "forge", "keys"), nil
}

// keyFileNames
const (
	privFileName = "forge.key"
	pubFileName  = "forge.pub"
)

// Keygen generates a new ed25519 keypair under dir.
// dir is the keys directory (e.g. from KeysDir()). It creates forge.key
// (0600) and forge.pub (0644). If keys already exist and overwrite is false,
// it returns an error. If overwrite is true, it replaces them.
func KeygenAt(dir string, overwrite bool) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create keys dir: %w", err)
	}
	privPath := filepath.Join(dir, privFileName)
	pubPath := filepath.Join(dir, pubFileName)
	if !overwrite {
		if _, err := os.Stat(privPath); err == nil {
			return nil, nil, fmt.Errorf("key already exists at %q (use --force to overwrite)", privPath)
		}
		if _, err := os.Stat(pubPath); err == nil {
			return nil, nil, fmt.Errorf("key already exists at %q (use --force to overwrite)", pubPath)
		}
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	privB64 := base64.StdEncoding.EncodeToString(priv)
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	// Write private key with 0600, public with 0644.
	if err := os.WriteFile(privPath, []byte(privB64+"\n"), 0o600); err != nil {
		return nil, nil, fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(pubB64+"\n"), 0o644); err != nil {
		return nil, nil, fmt.Errorf("write public key: %w", err)
	}
	return pub, priv, nil
}

// Keygen creates a keypair in the default user config location.
func Keygen(overwrite bool) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	dir, err := KeysDir()
	if err != nil {
		return nil, nil, err
	}
	return KeygenAt(dir, overwrite)
}

// LoadAnchor reads the public key anchor from the default location.
// Returns (pub, true, nil) if present, (nil, false, nil) if not installed.
func LoadAnchor() (ed25519.PublicKey, bool, error) {
	dir, err := KeysDir()
	if err != nil {
		return nil, false, err
	}
	return LoadAnchorAt(dir)
}

// LoadAnchorAt reads the anchor from a specific directory.
func LoadAnchorAt(dir string) (ed25519.PublicKey, bool, error) {
	pubPath := filepath.Join(dir, pubFileName)
	data, err := os.ReadFile(pubPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read anchor: %w", err)
	}
	b64 := stripSpace(string(data))
	if b64 == "" {
		return nil, false, fmt.Errorf("anchor file empty")
	}
	pubBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, false, fmt.Errorf("decode anchor: %w", err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return nil, false, fmt.Errorf("anchor size %d", len(pubBytes))
	}
	return ed25519.PublicKey(pubBytes), true, nil
}

// LoadPrivateKey reads the private key from the default location.
func LoadPrivateKey() (ed25519.PrivateKey, error) {
	dir, err := KeysDir()
	if err != nil {
		return nil, err
	}
	return LoadPrivateKeyAt(dir)
}

// LoadPrivateKeyAt reads the private key from a specific directory.
func LoadPrivateKeyAt(dir string) (ed25519.PrivateKey, error) {
	privPath := filepath.Join(dir, privFileName)
	data, err := os.ReadFile(privPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("private key not found at %q (run forge keygen first)", privPath)
		}
		return nil, fmt.Errorf("read private key: %w", err)
	}
	b64 := stripSpace(string(data))
	if b64 == "" {
		return nil, fmt.Errorf("private key file empty")
	}
	privBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	if len(privBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key size %d", len(privBytes))
	}
	return ed25519.PrivateKey(privBytes), nil
}

// Fingerprint returns a hex sha256 fingerprint of the public key bytes.
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

func stripSpace(s string) string {
	// Trim whitespace including newline.
	out := ""
	for _, r := range s {
		if r != ' ' && r != '\n' && r != '\r' && r != '\t' {
			out += string(r)
		}
	}
	return out
}
