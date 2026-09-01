package pluginwasm

import "errors"

// Sentinel errors for the plugin runtime.
var (
	// ErrABIMismatch is returned when a plugin's forge_abi_version does not equal plugin.ABIVersion.
	ErrABIMismatch = errors.New("plugin ABI version mismatch")

	// ErrChecksumMismatch is returned when an external plugin's SHA256 does not match its manifest checksum.
	ErrChecksumMismatch = errors.New("plugin checksum mismatch")

	// ErrApprovalRequired is returned when an external plugin is loaded without explicit approval (RNF-4.6 fail-closed).
	ErrApprovalRequired = errors.New("external plugin requires explicit approval")

	// ErrNotLoaded is returned when Enable/Disable references a plugin that was not loaded.
	ErrNotLoaded = errors.New("plugin not loaded")

	// ErrAlreadyEnabled is returned when Enable is called on an already-enabled plugin.
	ErrAlreadyEnabled = errors.New("plugin already enabled")

	// ErrNotEnabled is returned when Disable is called on a plugin that is not enabled.
	ErrNotEnabled = errors.New("plugin not enabled")

	// ErrCorruptedWASM is returned when wasm bytes fail to compile or instantiate (not a panic).
	ErrCorruptedWASM = errors.New("corrupted wasm module")
)
