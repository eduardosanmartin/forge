package approval

// Package approval implements ed25519-signed approval records (v2) with
// backward compatibility to v1 hash-only records (approved.flag).
//
// Trust model: signatures prove OPERATOR IDENTITY, not CA-grade chain of
// trust. The trust anchor is the public key stored in the user config
// directory (os.UserConfigDir()/forge/keys/forge.pub next to forge.key).
// A keypair is per-user and cross-project. Private key lives in
// USER-LEVEL config: %AppData%\forge\keys on Windows,
// ~/.config/forge/keys on Linux. Never inside any project tree.
//
// A verifier with no anchor installed treats v2 records as
// untrusted-but-well-formed (same as v1 + warning). Without an anchor,
// v2 gives no trust advantage over v1: fallback to hash check.
//
// Canonical serialization: the signature is computed over JSON marshaling
// of a fixed struct containing all fields except sig with stable field
// order (v, alg, artifact, name, sha256, ts, pub). encoding/json on a
// fixed struct is deterministic, so those bytes are the canonical
// representation. Documented here as the signing convention.
