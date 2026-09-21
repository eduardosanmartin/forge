// Package config implements forge's versioned, mergeable configuration.
//
// Configuration is layered field-group-wise: built-in defaults are overridden
// by the global file, which is overridden by the project file (or an explicit
// --config path). Later files replace earlier values per section; named
// provider entries are replaced wholesale. Missing files are skipped
// silently, but present-yet-invalid files are hard errors.
//
// All path fields ("storage.path", "logging.file") have a leading "~/"
// expanded at load time via os.UserHomeDir.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/eduardosanmartin/forge/internal/pathmatch"
)

// CurrentSchemaVersion is the newest configuration schema revision this
// build understands. Version 2 added the "permissions" section (RNF-4.1).
// Version 3 added permissions.shell.require_isolation (RNF-4.7), which
// makes Linux refuse shell_exec when OS-level isolation is unavailable
// instead of silently degrading; non-Linux platforms ignore it.
// Version 4 added the optional providers.<name>.model_roles map for
// cost-based model routing (RF-2.4/2.5); a v3 document needs no data
// change to be valid v4.
const CurrentSchemaVersion = 4

// Provider describes a single inference endpoint.
// Supported kinds (config providers.<name>.kind):
//   - "openai-compatible" — OpenAI-compatible chat completions (Ollama, OpenCode Zen, etc.)
//     Example: {"kind":"openai-compatible","base_url":"http://127.0.0.1:11434/v1","models":["qwen2.5-coder:7b"]}
//   - "anthropic" — Anthropic Messages API (https://api.anthropic.com)
//     Example: {"kind":"anthropic","base_url":"https://api.anthropic.com","models":["claude-3-5-sonnet-20241022"],"api_key":"sk-ant-..."}
//     Default base_url https://api.anthropic.com when empty. Requires x-api-key + anthropic-version:2023-06-01 headers.
//   - "gemini" — Google Gemini generateContent API (https://generativelanguage.googleapis.com)
//     Example: {"kind":"gemini","base_url":"https://generativelanguage.googleapis.com","models":["gemini-1.5-pro"],"api_key":"AIza..."}
//     Default base_url https://generativelanguage.googleapis.com when empty. Auth via x-goog-api-key header (never query param).
type Provider struct {
	Kind       string            `json:"kind"`
	BaseURL    string            `json:"base_url"`
	Models     []string          `json:"models"`
	ModelRoles map[string]string `json:"model_roles,omitempty"`
	// APIKey authenticates against remote endpoints. Empty falls back to the
	// OPENCODE_API_KEY env var so secrets stay out of config files.
	APIKey string `json:"api_key"`
	// PricePerMillionInputTokens/OutputTokens estimate cost for paid
	// providers (RNF-6.3: "métricas de costo... cuando aplique (modelos de
	// pago)"). Both zero (the default, including for every local/free
	// provider) means "not priced" — internal/cost omits a cost estimate
	// entirely rather than reporting a misleading $0.
	PricePerMillionInputTokens  float64 `json:"price_per_million_input_tokens,omitempty"`
	PricePerMillionOutputTokens float64 `json:"price_per_million_output_tokens,omitempty"`
	// RequestTimeoutSeconds bounds one chat completion HTTP call to this
	// provider. 0/unset falls back to DefaultRequestTimeoutSeconds (15 min)
	// — previously this was a single hardcoded 15-minute constant shared by
	// every provider regardless of profile, which meant a hung local model
	// blocked a turn for the same 15 minutes as a legitimately slow remote
	// one. Set this low (e.g. 120-180) for a local model known to be fast,
	// or raise it for a large remote model under real load.
	RequestTimeoutSeconds int `json:"request_timeout_seconds,omitempty"`
}

// DefaultRequestTimeoutSeconds is the fallback per-request HTTP timeout when
// a provider does not set request_timeout_seconds (900s = 15 minutes, the
// value every provider used unconditionally before this field existed).
const DefaultRequestTimeoutSeconds = 900

// StorageConfig locates forge's local database.
type StorageConfig struct {
	Path string `json:"path"`
}

// NetworkConfig bounds outbound network access.
type NetworkConfig struct {
	AllowedHosts []string `json:"allowed_hosts"`
}

// LoggingConfig selects log verbosity and an optional extra destination.
type LoggingConfig struct {
	Level string `json:"level"`
	File  string `json:"file"`
}

// DaemonConfig configures the daemon transport's remote-access safety floor
// (RF-7.4/RNF-4.11). A loopback --addr needs neither field: today's default
// (unauthenticated, plain HTTP on 127.0.0.1) is unchanged. Binding to a
// non-loopback address is refused unless BOTH are set — see the bind-address
// check in internal/daemon.
type DaemonConfig struct {
	// Addr is the default listen address (host:port) `forge serve` binds to
	// when --addr isn't passed explicitly on the command line. Empty falls
	// back to the CLI flag's own default (127.0.0.1:0, an ephemeral port).
	// Setting this lets a project pin a stable local port instead of getting
	// a new one on every restart.
	Addr string `json:"addr,omitempty"`
	// AuthTokenHash is SHA-256(token) as lowercase hex. The raw token is
	// never persisted — `forge daemon set-password` writes only this hash.
	// Empty means auth is disabled (only valid for a loopback bind).
	AuthTokenHash string `json:"auth_token_hash,omitempty"`
	// TLSCertFile/TLSKeyFile is a PEM certificate+key pair the transport
	// serves over. Both empty means plain HTTP (only valid for a loopback
	// bind). `forge serve --tls-self-signed` populates these at runtime
	// without persisting them to the config file.
	TLSCertFile string `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `json:"tls_key_file,omitempty"`
}

// FSPermissions bounds filesystem access with glob patterns. Relative
// patterns match workspace-relative paths; absolute patterns (POSIX-rooted
// or drive-letter form, forward-slashed) are the documented escape hatch for
// explicitly authorized out-of-workspace locations.
type FSPermissions struct {
	Read  []string `json:"read"`
	Write []string `json:"write"`
}

// ShellPermissions allows shell executables by base name (case-insensitive).
// RequireIsolation (schema v3, RNF-4.7) asks forge to refuse shell
// execution on Linux when OS-level isolation (Landlock + seccomp wrapper)
// is unavailable, rather than degrading to permissions-only enforcement.
// Non-Linux platforms ignore the flag: macOS v0 and Windows are
// documented as permissions-only per spec §6.
type ShellPermissions struct {
	Allow            []string `json:"allow"`
	RequireIsolation bool     `json:"require_isolation"`
}

// GitPermissions allows git subcommands (lowercase convention). Destructive
// invocations stay blocked by the engine's non-configurable safety floor
// regardless of this list (RNF-8.2).
type GitPermissions struct {
	Allow []string `json:"allow"`
}

// GitHubPermissions allows the fixed read-only github tool subcommands
// (RF-10.3): "issue-list", "issue-view", "pr-list", "pr-view". Empty by
// default — deny-by-default like git/shell, not floor-allowed like the
// custom kind, since this tool reaches the network via the `gh` CLI.
type GitHubPermissions struct {
	Allow []string `json:"allow"`
}

// CustomPermissions arbitrates forge-internal harness tools (kind "custom")
// by tool name (case-sensitive). It mirrors perms.CustomPermissions:
//
//   - deny turns any custom tool off, mutating or not;
//   - allow restores one of the persistent-memory-mutating tools
//     (anchoring_store, anchoring_delete) that the engine's custom write
//     floor denies by default (RNF-4.12: a model-proposed anchor must never
//     be anchored automatically);
//   - an allow entry on a non-mutating (read) tool is a no-op — those tools
//     are already floor-allowed;
//   - a tool present in both lists resolves to DENY (fail-closed).
type CustomPermissions struct {
	Deny  []string `json:"deny"`
	Allow []string `json:"allow"`
}

// PermissionsPolicy mirrors the "permissions" section of the config
// document. It is deny-by-default: anything not explicitly allowed is
// refused by the permission engine (RNF-4.1).
type PermissionsPolicy struct {
	FS     FSPermissions     `json:"fs"`
	Shell  ShellPermissions  `json:"shell"`
	Git    GitPermissions    `json:"git"`
	GitHub GitHubPermissions `json:"github"`
	Custom CustomPermissions `json:"custom"`
}

// defaultPermissionsPolicy returns the built-in baseline policy:
// workspace-wide filesystem access under the default deny posture for
// everything else, an EMPTY shell allowlist (RNF-4.1: nothing runs unless
// declared) with OS isolation required on capable platforms (RNF-4.7), and
// a conventional read-only-plus-staging git allowlist.
func defaultPermissionsPolicy() PermissionsPolicy {
	return PermissionsPolicy{
		FS:    FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
		Shell: ShellPermissions{Allow: []string{}, RequireIsolation: true},
		Git: GitPermissions{Allow: []string{
			"status", "add", "commit", "log", "diff", "branch",
			"switch", "stash", "restore", "show", "remote", "fetch",
		}},
	}
}

// Streaming mode constants govern llm.streaming.mode (auto|on|off).
// Default is "off" for backward compatibility (v2 behavior). Safe transition:
// legacy `llm.streaming: true/false` is still accepted (true => "on", false => "off").
// New configs should use `llm.streaming: {"mode":"auto"}`.
//   - "off": never stream, always use Chat.
//   - "on": always attempt ChatStream; ErrStreamingNotSupported fallback to Chat before first token, mid-stream failure fails turn.
//   - "auto": try streaming, fallback to Chat only when ErrStreamingNotSupported before first token; mid-stream failure still fails turn.
const (
	StreamingModeOff  = "off"
	StreamingModeOn   = "on"
	StreamingModeAuto = "auto"
)

// StreamingConfig holds streaming mode; JSON path is llm.streaming (object with mode) or legacy bool.
type StreamingConfig struct {
	Mode string `json:"mode"`
}

// UnmarshalJSON accepts bool (legacy), string, or object {mode: ...} for backward compatibility.
func (s *StreamingConfig) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		s.Mode = StreamingModeOff
		return nil
	}
	// Try bool first (legacy).
	var b bool
	if err := json.Unmarshal(trimmed, &b); err == nil {
		if b {
			s.Mode = StreamingModeOn
		} else {
			s.Mode = StreamingModeOff
		}
		return nil
	}
	// Try string ("auto"/"on"/"off").
	var str string
	if err := json.Unmarshal(trimmed, &str); err == nil {
		m := strings.ToLower(strings.TrimSpace(str))
		switch m {
		case StreamingModeOff, StreamingModeOn, StreamingModeAuto:
			s.Mode = m
			return nil
		default:
			return fmt.Errorf("streaming.mode must be one of auto|on|off, got %q", str)
		}
	}
	// Try object {mode: ...}
	var obj struct {
		Mode *string `json:"mode"`
	}
	if err := json.Unmarshal(trimmed, &obj); err == nil {
		if obj.Mode == nil || strings.TrimSpace(*obj.Mode) == "" {
			s.Mode = StreamingModeOff
			return nil
		}
		m := strings.ToLower(strings.TrimSpace(*obj.Mode))
		switch m {
		case StreamingModeOff, StreamingModeOn, StreamingModeAuto:
			s.Mode = m
			return nil
		default:
			return fmt.Errorf("streaming.mode must be one of auto|on|off, got %q", *obj.Mode)
		}
	}
	return fmt.Errorf("invalid streaming config: %s", string(trimmed))
}

// MarshalJSON always emits object form {"mode": "..."} for forward compatibility.
func (s StreamingConfig) MarshalJSON() ([]byte, error) {
	mode := s.Mode
	if mode == "" {
		mode = StreamingModeOff
	}
	return json.Marshal(map[string]string{"mode": mode})
}

// IsEnabled reports whether streaming should be attempted (mode != off).
func (s StreamingConfig) IsEnabled() bool { return s.Mode != "" && s.Mode != StreamingModeOff }

// IsAuto reports whether mode is auto (try streaming, fallback only on ErrStreamingNotSupported).
func (s StreamingConfig) IsAuto() bool { return s.Mode == StreamingModeAuto }

// LLMConfig holds LLM-related global toggles.
// Streaming (WU3): optional, default OFF (Mode="off") for backward compatibility.
// When enabled, the daemon/agent may use ChatStream and publish message.delta.event
// notifications for live TUI updates. Mid-stream failure fails the turn; only
// ErrStreamingNotSupported before first token degrades to Chat.
type LLMConfig struct {
	Streaming StreamingConfig `json:"streaming"`
	// Cores (RNF-1.6, optional): logical cores the local inference engine is
	// allowed to use. 0/unset means the default: DefaultInferenceCores
	// (max(1, NumCPU-2) on the machine's logical cores, keeping room for
	// the OS and the user). An explicit value — including one that saturates the
	// whole machine — is honored as-is; explicit user intent needs no
	// warning (spec RNF-1.6: default leaves margin "unless the user
	// explicitly states otherwise").
	//
	// Honest limitation: forge is a CLIENT of inference servers, not their
	// launcher. The current provider surface (OpenAI-compatible /v1 chat)
	// carries no per-request threading knob like Ollama's native
	// /api/chat "options"."num_thread", so forge cannot directly enforce
	// this budget over the wire today. The effective value is computed at
	// daemon startup (cli runServe) and surfaced as a structured log so
	// operators can pass it to the inference server's own settings
	// (e.g. Ollama OMP_NUM_THREADS) or plan system capacity. Should a
	// native API provider surface appear, this field is the wiring point.
	Cores int `json:"cores"`
}

// TUIConfig holds TUI preferences persisted in .forge/config.json.
// Added for TUI-1: layout, palette, sidebar. Defaults are hybrid/ember/true.
type TUIConfig struct {
	Layout  string `json:"layout"`
	Palette string `json:"palette"`
	Sidebar bool   `json:"sidebar"`
}

// LimitsConfig bounds artifact sizes at install time (WU7).
// plugin_wasm_max_bytes caps the plugin .wasm entrypoint (default 2 MiB).
// skill_file_max_bytes caps each file inside a skill directory (default 1 MiB).
// Zero or negative values in a config file are invalid and fall back to
// defaults (handled in mergeInto); missing limits section preserves built-in
// defaults. Message store size cap is deferred (not implemented).
type LimitsConfig struct {
	PluginWasmMaxBytes int64 `json:"plugin_wasm_max_bytes"`
	SkillFileMaxBytes  int64 `json:"skill_file_max_bytes"`
}

// Default artifact size caps (WU7, owner decision).
const (
	DefaultPluginWasmMaxBytes int64 = 2 * 1024 * 1024 // 2 MiB
	DefaultSkillFileMaxBytes  int64 = 1 * 1024 * 1024 // 1 MiB
)

// AgentConfig bounds the agent turn loop (TUI-6).
// MaxIterations caps tool-call iterations per turn (default 10).
// MaxTurnSeconds caps total wall-clock seconds per turn (default 300): a hung
// provider fails the turn visibly instead of locking the UI forever.
// MaxParallelChildren caps concurrent child subagent turns (RF-1.2): bounded
// worker pool size 2-4 (default 2) — parallelizes LLM calls while SQLite writes
// are serialized via single connection + WAL busy_timeout. Distinct branched
// sessions avoid sharing the same write txn.
// Zero or negative values in a config file are invalid and fall back to
// defaults (handled in mergeInto), matching the LimitsConfig pattern.
type AgentConfig struct {
	MaxIterations       int `json:"max_iterations"`
	MaxTurnSeconds      int `json:"max_turn_seconds"`
	MaxParallelChildren int `json:"max_parallel_children"`
}

// Default agent caps (TUI-6, owner decision; timeout added retest-5).
const DefaultAgentMaxIterations = 10

// DefaultAgentMaxTurnSeconds bounds a turn at 5 minutes: well above healthy
// slow turns on free tiers (~2min observed), far below a real hang.
const DefaultAgentMaxTurnSeconds = 300

// DefaultAgentMaxParallelChildren bounds concurrent subagent turns (RF-1.2).
const DefaultAgentMaxParallelChildren = 2

// AgentMaxParallelChildren bounds (RF-1.2 spec: pool 2-4).
const AgentMaxParallelChildrenMin = 2
const AgentMaxParallelChildrenMax = 4

// ProjectConfig holds project sensitivity classification (RNF-9).
// Sensitivity is a ceiling on autonomy (general | regulado | datos-sensibles).
// Canonical values are the spec's Spanish terms; English aliases low/medium/high
// and regulated/sensitive are normalized to the canonical forms on Validate/Load.
// SkillsConfig controls how skills get activated for an agent turn (RF-4.2).
//
// LazyLoad true selects semantic matching: Skills.Relevant(userMessage)
// decides per turn which enabled skills to inject, scored against an
// embedding. LazyLoad false selects manual activation instead: no matching
// call happens at all — the active set is exactly Enabled (skill names from
// this project's config) plus every skill found in the global skills
// directory (~/.forge/skills — see config.GlobalSkillsDir), which is always
// active across every project without needing to be listed here. A skill
// present in both the project and the global directory with the same name
// resolves to the project's copy (more specific wins).
//
// Default is false: until the embedding backend behind LazyLoad is a real
// semantic model (not the bag-of-words placeholder), semantic matching
// essentially never fires for realistic prompts — manual activation is the
// only mode that reliably works today.
type SkillsConfig struct {
	LazyLoad bool     `json:"lazy_load"`
	Enabled  []string `json:"enabled,omitempty"`
}

// EmbeddingsConfig controls the optional real embedding backend
// (hojaDeRuta-embeddings-skills.md Fase 4) that skills.lazy_load's
// semantic matching (and retrieval, RF-3.2) need to be useful in
// practice — the bag-of-words hash they fall back to otherwise scores
// real queries far below any reasonable threshold (measured live: ~0.08
// on realistic Spanish prompts). Both LlamaServerPath and ModelPath must
// point to files that actually exist for the daemon to start the
// backend; if either is missing, empty, or the process fails its health
// check, the daemon logs that and continues with the hash — this is
// never a fatal startup error.
//
// This is deliberately NOT auto-downloaded: the daemon only starts a
// backend it finds already in place. Fetching a several-hundred-MB model
// file on first run, with progress reporting and checksum verification,
// is real scope left for later — see hojaDeRuta-embeddings-skills.md
// Fase 0's still-open "mecanismo de distribución del modelo" question.
type EmbeddingsConfig struct {
	Enabled         bool   `json:"enabled"`
	LlamaServerPath string `json:"llama_server_path"`
	ModelPath       string `json:"model_path"`
	// Port to run llama-server on. 0 (the default) picks an ephemeral
	// port, same convention as DaemonConfig.Addr's ":0".
	Port int `json:"port,omitempty"`
}

type ProjectConfig struct {
	Sensitivity string `json:"sensitivity"`
	// SpecPath is the optional workspace-relative path to the project spec
	// file (RF-8.4). Empty = probe the workspace root defaults.
	SpecPath string `json:"spec_path"`
}

// Sensitivity canonical values (RNF-9.1, spec §7.2).
const (
	SensitivityGeneral   = "general"
	SensitivityRegulated = "regulado"
	SensitivitySensitive = "datos-sensibles"
)

// Config is the full forge configuration document.
type Config struct {
	SchemaVersion   int                 `json:"schema_version"`
	DefaultProvider string              `json:"default_provider"`
	Providers       map[string]Provider `json:"providers"`
	Storage         StorageConfig       `json:"storage"`
	Network         NetworkConfig       `json:"network"`
	Logging         LoggingConfig       `json:"logging"`
	Permissions     PermissionsPolicy   `json:"permissions"`
	TUI             TUIConfig           `json:"tui"`
	LLM             LLMConfig           `json:"llm"`
	Limits          LimitsConfig        `json:"limits"`
	Agent           AgentConfig         `json:"agent"`
	Project         ProjectConfig       `json:"project"`
	Daemon          DaemonConfig        `json:"daemon"`
	Skills          SkillsConfig        `json:"skills"`
	Embeddings      EmbeddingsConfig    `json:"embeddings"`
	// FallbackChain is an ordered list of "provider/model" entries (same
	// syntax as forge fanout --models) tried in order on a RETRYABLE
	// failure — rate limit (429), transient upstream outage (502/503/504),
	// or a network timeout/connection error (see llm.RetryableError). A
	// non-retryable failure (bad request, auth, model not found) stops the
	// chain immediately: swapping models can't fix a malformed request.
	// Empty/absent (the default) disables failover entirely — behavior is
	// unchanged from before this field existed. See internal/llm's registry
	// for where the chain is actually walked.
	FallbackChain []string `json:"fallback_chain,omitempty"`
}

// Defaults returns the built-in baseline configuration. Callers may treat the
// returned value as a fresh, unshared instance.
func Defaults() *Config {
	return &Config{
		SchemaVersion:   CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: "http://127.0.0.1:11434/v1",
				Models:  []string{"qwen2.5-coder:7b"},
				ModelRoles: map[string]string{
					"cheap":      "qwen2.5-coder:1.5b",
					"generation": "qwen2.5-coder:7b",
					"reasoning":  "relational/VULCAN",
				},
			},
		},
		Storage:     StorageConfig{Path: "~/.forge/forge.db"},
		Network:     NetworkConfig{AllowedHosts: []string{"127.0.0.1", "localhost"}},
		Logging:     LoggingConfig{Level: "info", File: ""},
		Permissions: defaultPermissionsPolicy(),
		TUI:         TUIConfig{Layout: "hybrid", Palette: "ember", Sidebar: true},
		LLM:         LLMConfig{Streaming: StreamingConfig{Mode: StreamingModeOff}},
		Limits: LimitsConfig{
			PluginWasmMaxBytes: DefaultPluginWasmMaxBytes,
			SkillFileMaxBytes:  DefaultSkillFileMaxBytes,
		},
		Agent:      AgentConfig{MaxIterations: DefaultAgentMaxIterations, MaxTurnSeconds: DefaultAgentMaxTurnSeconds, MaxParallelChildren: DefaultAgentMaxParallelChildren},
		Project:    ProjectConfig{Sensitivity: SensitivityGeneral},
		Skills:     SkillsConfig{LazyLoad: false},
		Embeddings: defaultEmbeddingsConfig(),
	}
}

// defaultEmbeddingsConfig points at the conventional ~/.forge/embeddings/
// location (paralleling ~/.forge/keys, ~/.forge/skills) — Enabled true, but
// the daemon only actually starts the backend if both files are really
// there (EmbeddingsConfig's doc comment). Best-effort: if the home
// directory can't be resolved, leaves the paths empty, which has the same
// effect (daemon finds nothing there, falls back to the hash).
func defaultEmbeddingsConfig() EmbeddingsConfig {
	base, err := ExpandPath("~/.forge/embeddings")
	if err != nil {
		return EmbeddingsConfig{Enabled: true}
	}
	binName := "llama-server"
	if runtime.GOOS == "windows" {
		binName = "llama-server.exe"
	}
	return EmbeddingsConfig{
		Enabled:         true,
		LlamaServerPath: filepath.Join(base, binName),
		ModelPath:       filepath.Join(base, "models", "bge-m3-q4_k_m.gguf"),
	}
}

// ExpandPath expands a leading "~/" (or bare "~") in p to the current user's
// home directory and normalizes separators. Paths without the prefix are
// returned unchanged. Expansion failure (for example, when no home directory
// is defined) returns an error rather than a silently mangled path.
func ExpandPath(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expand path %q: resolve home directory: %w", p, err)
	}
	if p == "~" {
		return home, nil
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, "~/"), `~\`)
	return filepath.Join(home, filepath.FromSlash(rest)), nil
}

// GlobalConfigPath returns the user-wide configuration path
// (~/.forge/config.json), with "~" expanded.
func GlobalConfigPath() (string, error) {
	p, err := ExpandPath("~/.forge/config.json")
	if err != nil {
		return "", fmt.Errorf("resolve global config path: %w", err)
	}
	return p, nil
}

// GlobalSkillsDir returns the user-wide skills directory (~/.forge/skills),
// with "~" expanded. Skills found here are active in every project's manual
// activation set (SkillsConfig.LazyLoad == false) without needing to be
// listed in any project's own config — see SkillsConfig's doc comment.
func GlobalSkillsDir() (string, error) {
	p, err := ExpandPath("~/.forge/skills")
	if err != nil {
		return "", fmt.Errorf("resolve global skills dir: %w", err)
	}
	return p, nil
}

// ProjectConfigPath returns the project-scoped configuration path
// (./.forge/config.json).
func ProjectConfigPath() (string, error) {
	return ExpandPath(filepath.Join(".", ".forge", "config.json"))
}

// filePermissions mirrors PermissionsPolicy with presence-tracking pointers
// so merging can replace each subsection (fs/shell/git/custom) wholesale only
// when that subsection is present in an overriding document.
type filePermissions struct {
	FS     *FSPermissions     `json:"fs"`
	Shell  *ShellPermissions  `json:"shell"`
	Git    *GitPermissions    `json:"git"`
	GitHub *GitHubPermissions `json:"github"`
	Custom *CustomPermissions `json:"custom"`
}

// fileLimits mirrors LimitsConfig with presence-tracking pointers so that
// merging can distinguish "field absent" from "field set to zero value".
// Zero/negative values are treated as invalid and fall back to defaults.
type fileLimits struct {
	PluginWasmMaxBytes *int64 `json:"plugin_wasm_max_bytes"`
	SkillFileMaxBytes  *int64 `json:"skill_file_max_bytes"`
}

// fileAgent mirrors AgentConfig with presence-tracking pointers so that
// merging can distinguish "field absent" from "field set to zero value".
// Zero/negative values are treated as invalid and fall back to defaults.
type fileAgent struct {
	MaxIterations       *int `json:"max_iterations"`
	MaxTurnSeconds      *int `json:"max_turn_seconds"`
	MaxParallelChildren *int `json:"max_parallel_children"`
}

// fileConfig mirrors Config with presence-tracking pointers so that merging
// can distinguish "field absent" from "field set to zero value".
type fileConfig struct {
	SchemaVersion   *int                `json:"schema_version"`
	DefaultProvider *string             `json:"default_provider"`
	Providers       map[string]Provider `json:"providers"`
	Storage         *StorageConfig      `json:"storage"`
	Network         *NetworkConfig      `json:"network"`
	Logging         *LoggingConfig      `json:"logging"`
	Permissions     *filePermissions    `json:"permissions"`
	TUI             *TUIConfig          `json:"tui"`
	LLM             *LLMConfig          `json:"llm"`
	Limits          *fileLimits         `json:"limits"`
	Agent           *fileAgent          `json:"agent"`
	Project         *ProjectConfig      `json:"project"`
	Daemon          *DaemonConfig       `json:"daemon"`
	Skills          *SkillsConfig       `json:"skills"`
	Embeddings      *EmbeddingsConfig   `json:"embeddings"`
	// FallbackChain: no pointer needed — nil (key absent from this layer)
	// vs non-nil (key present, even as "[]" to explicitly clear a lower
	// layer's chain) is already exactly what json.Unmarshal gives a plain
	// slice field, same as Providers above.
	FallbackChain []string `json:"fallback_chain"`
}

// Load builds a Config from defaults overlaid with the given files in order:
// later files override earlier values field-group-wise (provider entries are
// replaced wholesale per named provider; scalar sections are replaced whole
// whenever present; permissions subsections fs/shell/git/custom each replace
// whole when present). Missing files are skipped silently; present but invalid
// files produce an error that names the offending path. Documents older than
// the current schema version are migrated forward before decoding; the
// returned Config always describes current-schema semantics.
func Load(filePaths ...string) (*Config, error) {
	cfg := Defaults()

	for _, path := range filePaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read config file %s: %w", path, err)
		}

		// Probe the schema version first (with unknown-field rejection, so a
		// typo still fails fast even before migration).
		var probe fileConfig
		probeDec := json.NewDecoder(bytes.NewReader(raw))
		probeDec.DisallowUnknownFields()
		if err := probeDec.Decode(&probe); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}

		version := CurrentSchemaVersion
		if probe.SchemaVersion != nil {
			version = *probe.SchemaVersion
		}
		if version < 1 || version > CurrentSchemaVersion {
			return nil, fmt.Errorf(
				"config file %s: unsupported schema_version %d (supported range: %d..%d)",
				path, version, 1, CurrentSchemaVersion,
			)
		}

		doc := raw
		if version != CurrentSchemaVersion {
			migrated, err := Migrate(raw, version)
			if err != nil {
				return nil, fmt.Errorf("config file %s: %w", path, err)
			}
			doc = migrated
		}

		var fc fileConfig
		dec := json.NewDecoder(bytes.NewReader(doc))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fc); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}

		mergeInto(cfg, &fc)

		// A migrated document now describes current-schema semantics in
		// memory (the file on disk is untouched until Save).
		if version != CurrentSchemaVersion {
			cfg.SchemaVersion = CurrentSchemaVersion
		}
	}

	if err := cfg.expandPaths(); err != nil {
		return nil, err
	}
	// Normalize limits: zero/negative values are invalid and fall back to
	// defaults (covers direct Config construction bypassing mergeInto).
	if cfg.Limits.PluginWasmMaxBytes <= 0 {
		cfg.Limits.PluginWasmMaxBytes = DefaultPluginWasmMaxBytes
	}
	if cfg.Limits.SkillFileMaxBytes <= 0 {
		cfg.Limits.SkillFileMaxBytes = DefaultSkillFileMaxBytes
	}
	if cfg.Agent.MaxIterations <= 0 {
		cfg.Agent.MaxIterations = DefaultAgentMaxIterations
	}
	if cfg.Agent.MaxTurnSeconds <= 0 {
		cfg.Agent.MaxTurnSeconds = DefaultAgentMaxTurnSeconds
	}
	if cfg.Agent.MaxParallelChildren <= 0 {
		cfg.Agent.MaxParallelChildren = DefaultAgentMaxParallelChildren
	}
	if cfg.Agent.MaxParallelChildren < AgentMaxParallelChildrenMin {
		cfg.Agent.MaxParallelChildren = AgentMaxParallelChildrenMin
	}
	if cfg.Agent.MaxParallelChildren > AgentMaxParallelChildrenMax {
		cfg.Agent.MaxParallelChildren = AgentMaxParallelChildrenMax
	}
	// Normalize LLM streaming mode: empty defaults to off, lowercase, validate.
	if strings.TrimSpace(cfg.LLM.Streaming.Mode) == "" {
		cfg.LLM.Streaming.Mode = StreamingModeOff
	} else {
		m := strings.ToLower(strings.TrimSpace(cfg.LLM.Streaming.Mode))
		switch m {
		case StreamingModeOff, StreamingModeOn, StreamingModeAuto:
			cfg.LLM.Streaming.Mode = m
		default:
			// Leave invalid for Validate to report; don't silently correct.
			cfg.LLM.Streaming.Mode = m
		}
	}
	// Normalize sensitivity alias to canonical form and default empty to general.
	if normalized, ok := normalizeSensitivity(cfg.Project.Sensitivity); ok {
		cfg.Project.Sensitivity = normalized
	} else if strings.TrimSpace(cfg.Project.Sensitivity) == "" {
		cfg.Project.Sensitivity = SensitivityGeneral
	}
	return cfg, nil
}

// mergeInto applies every field group present in fc onto dst.
func mergeInto(dst *Config, fc *fileConfig) {
	if fc.SchemaVersion != nil {
		dst.SchemaVersion = *fc.SchemaVersion
	}
	if fc.DefaultProvider != nil {
		dst.DefaultProvider = *fc.DefaultProvider
	}
	for name, p := range fc.Providers {
		dst.Providers[name] = p
	}
	if fc.FallbackChain != nil {
		dst.FallbackChain = fc.FallbackChain
	}
	if fc.Storage != nil {
		dst.Storage = *fc.Storage
	}
	if fc.Network != nil {
		dst.Network = *fc.Network
	}
	if fc.Logging != nil {
		dst.Logging = *fc.Logging
	}
	if fc.Permissions != nil {
		// Group-wise merge: each present subsection (fs/shell/git/custom)
		// replaces the corresponding policy group wholesale, mirroring how
		// scalar sections behave. Lists inside a present subsection are
		// taken as-is.
		fp := fc.Permissions
		if fp.FS != nil {
			dst.Permissions.FS = *fp.FS
		}
		if fp.Shell != nil {
			dst.Permissions.Shell = *fp.Shell
		}
		if fp.Git != nil {
			dst.Permissions.Git = *fp.Git
		}
		if fp.GitHub != nil {
			dst.Permissions.GitHub = *fp.GitHub
		}
		if fp.Custom != nil {
			dst.Permissions.Custom = *fp.Custom
		}
	}
	if fc.TUI != nil {
		dst.TUI = *fc.TUI
	}
	if fc.LLM != nil {
		dst.LLM = *fc.LLM
	}
	if fc.Limits != nil {
		// Limits merge: each present key replaces the corresponding value;
		// zero/negative values are invalid and fall back to defaults rather
		// than being accepted (documented in LimitsConfig).
		if fc.Limits.PluginWasmMaxBytes != nil {
			v := *fc.Limits.PluginWasmMaxBytes
			if v > 0 {
				dst.Limits.PluginWasmMaxBytes = v
			} else {
				dst.Limits.PluginWasmMaxBytes = DefaultPluginWasmMaxBytes
			}
		}
		if fc.Limits.SkillFileMaxBytes != nil {
			v := *fc.Limits.SkillFileMaxBytes
			if v > 0 {
				dst.Limits.SkillFileMaxBytes = v
			} else {
				dst.Limits.SkillFileMaxBytes = DefaultSkillFileMaxBytes
			}
		}
	}
	if fc.Agent != nil {
		if fc.Agent.MaxIterations != nil {
			v := *fc.Agent.MaxIterations
			if v > 0 {
				dst.Agent.MaxIterations = v
			} else {
				dst.Agent.MaxIterations = DefaultAgentMaxIterations
			}
		}
		if fc.Agent.MaxTurnSeconds != nil {
			v := *fc.Agent.MaxTurnSeconds
			if v > 0 {
				dst.Agent.MaxTurnSeconds = v
			} else {
				dst.Agent.MaxTurnSeconds = DefaultAgentMaxTurnSeconds
			}
		}
		if fc.Agent.MaxParallelChildren != nil {
			v := *fc.Agent.MaxParallelChildren
			if v <= 0 {
				dst.Agent.MaxParallelChildren = DefaultAgentMaxParallelChildren
			} else if v < AgentMaxParallelChildrenMin {
				dst.Agent.MaxParallelChildren = AgentMaxParallelChildrenMin
			} else if v > AgentMaxParallelChildrenMax {
				dst.Agent.MaxParallelChildren = AgentMaxParallelChildrenMax
			} else {
				dst.Agent.MaxParallelChildren = v
			}
		}
	}
	if fc.Project != nil {
		dst.Project = *fc.Project
	}
	if fc.Daemon != nil {
		dst.Daemon = *fc.Daemon
	}
	if fc.Skills != nil {
		dst.Skills = *fc.Skills
	}
	if fc.Embeddings != nil {
		dst.Embeddings = *fc.Embeddings
	}
}

// expandPaths expands "~" in every path field of c in place.
func (c *Config) expandPaths() error {
	expanded, err := ExpandPath(c.Storage.Path)
	if err != nil {
		return fmt.Errorf("config storage.path: %w", err)
	}
	c.Storage.Path = expanded

	expanded, err = ExpandPath(c.Logging.File)
	if err != nil {
		return fmt.Errorf("config logging.file: %w", err)
	}
	c.Logging.File = expanded
	return nil
}

// Migrate forwards raw config JSON from schema version from to the current
// schema version, applying the migration chain one step at a time. The
// current version returns data unchanged; versions without a migration step
// report an error naming both versions.
func Migrate(data []byte, from int) ([]byte, error) {
	switch from {
	case CurrentSchemaVersion:
		return data, nil
	case 1:
		// Chain: v1 gains the permissions section (v2), then the shell
		// isolation flag (v3), then the optional provider model_roles
		// map (v4, no data change).
		v2, err := migrateV1ToV2(data)
		if err != nil {
			return nil, err
		}
		v3, err := migrateV2ToV3(v2)
		if err != nil {
			return nil, err
		}
		return migrateV3ToV4(v3)
	case 2:
		v3, err := migrateV2ToV3(data)
		if err != nil {
			return nil, err
		}
		return migrateV3ToV4(v3)
	case 3:
		return migrateV3ToV4(data)
	default:
		return nil, fmt.Errorf(
			"no migration path from schema_version %d to schema_version %d",
			from, CurrentSchemaVersion,
		)
	}
}

// migrateV1ToV2 upgrades a v1 document to v2 by injecting the built-in
// permissions policy when the document predates the section (schema v2 added
// "permissions", RNF-4.1). All other fields are preserved verbatim; a
// document that already carries a permissions key is left untouched so
// hand-written forward-looking sections are never clobbered.
func migrateV1ToV2(data []byte) ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("migrate schema v1 -> v2: parse document: %w", err)
	}
	if _, exists := doc["permissions"]; exists {
		return data, nil
	}
	def, err := json.Marshal(defaultPermissionsPolicy())
	if err != nil {
		return nil, fmt.Errorf("migrate schema v1 -> v2: encode default permissions: %w", err)
	}
	doc["permissions"] = json.RawMessage(def)
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("migrate schema v1 -> v2: encode migrated document: %w", err)
	}
	return out, nil
}

// migrateV2ToV3 upgrades a v2 document to v3 by ensuring
// permissions.shell carries require_isolation=true (schema v3 added the
// flag, RNF-4.7). Absence in a v2 document meant "before the field
// existed", so it upgrades to the secure default rather than Go's zero
// value; an explicitly written false is preserved verbatim. Sibling fields
// (shell.allow, fs, git) and documents without a permissions section at all
// are left untouched — merging falls back to Defaults(), which already
// requires isolation.
func migrateV2ToV3(data []byte) ([]byte, error) {
	const step = "migrate schema v2 -> v3"
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: parse document: %w", step, err)
	}
	permsRaw, exists := doc["permissions"]
	if !exists {
		// No permissions section: merge supplies Defaults(), whose shell
		// policy requires isolation.
		return data, nil
	}

	var perms map[string]json.RawMessage
	if err := json.Unmarshal(permsRaw, &perms); err != nil {
		return nil, fmt.Errorf("%s: parse permissions section: %w", step, err)
	}

	var shell map[string]json.RawMessage
	if shellRaw, exists := perms["shell"]; exists {
		if err := json.Unmarshal(shellRaw, &shell); err != nil {
			return nil, fmt.Errorf("%s: parse permissions.shell section: %w", step, err)
		}
	} else {
		shell = make(map[string]json.RawMessage)
	}

	if _, exists := shell["require_isolation"]; !exists {
		shell["require_isolation"] = json.RawMessage("true")
	}

	shellOut, err := json.Marshal(shell)
	if err != nil {
		return nil, fmt.Errorf("%s: encode permissions.shell: %w", step, err)
	}
	perms["shell"] = json.RawMessage(shellOut)
	permsOut, err := json.Marshal(perms)
	if err != nil {
		return nil, fmt.Errorf("%s: encode permissions: %w", step, err)
	}
	doc["permissions"] = json.RawMessage(permsOut)

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: encode migrated document: %w", step, err)
	}
	return out, nil
}

// migrateV3ToV4 upgrades a v3 document to v4. Schema v4 added the OPTIONAL
// providers.<name>.model_roles map (Provider.ModelRoles, cost-based model
// routing, RF-2.4/2.5): absence in a v3 document already means "no roles
// assigned" (the Go zero value), so no data transformation is needed and
// the document passes through unchanged. The step exists so the migration
// chain can express v1 -> v4 and Load accepts v3 files instead of rejecting
// them with "no migration path" (the gap the v4 bump originally left).
func migrateV3ToV4(data []byte) ([]byte, error) {
	return data, nil
}

// Validate checks c against all configuration rules and returns an aggregated
// error listing every violation found (nil when the config is valid). An
// empty logging.level is normalized to "info" before validation. Path fields
// are expected to be pre-expanded (Load does this); validation only requires
// them to be non-empty.
func (c *Config) Validate() error {
	var violations []error

	if strings.TrimSpace(c.DefaultProvider) == "" {
		violations = append(violations, errors.New("default_provider must not be empty"))
	} else if _, ok := c.Providers[c.DefaultProvider]; !ok {
		violations = append(violations, fmt.Errorf(
			"default_provider %q does not match any entry in providers", c.DefaultProvider))
	}

	for name, p := range c.Providers {
		label := fmt.Sprintf("provider %q", name)
		switch p.Kind {
		case "openai-compatible", "anthropic", "gemini":
		default:
			violations = append(violations, fmt.Errorf(
				"%s: kind must be one of \"openai-compatible\", \"anthropic\", \"gemini\", got %q", label, p.Kind))
		}
		u, err := url.Parse(p.BaseURL)
		switch {
		case err != nil:
			violations = append(violations, fmt.Errorf(
				"%s: base_url %q is not a valid URL: %v", label, p.BaseURL, err))
		case u.Scheme != "http" && u.Scheme != "https":
			violations = append(violations, fmt.Errorf(
				"%s: base_url %q must be an absolute http(s) URL", label, p.BaseURL))
		case u.Host == "":
			violations = append(violations, fmt.Errorf(
				"%s: base_url %q is missing a host", label, p.BaseURL))
		}
		if len(p.Models) < 1 {
			violations = append(violations, fmt.Errorf(
				"%s: must declare at least one model", label))
		}
		if p.RequestTimeoutSeconds < 0 {
			violations = append(violations, fmt.Errorf(
				"%s: request_timeout_seconds must be >= 0, got %d", label, p.RequestTimeoutSeconds))
		}
	}

	for i, entry := range c.FallbackChain {
		providerName, model, ok := strings.Cut(entry, "/")
		if !ok || strings.TrimSpace(providerName) == "" || strings.TrimSpace(model) == "" {
			violations = append(violations, fmt.Errorf(
				"fallback_chain[%d] %q must be \"provider/model\"", i, entry))
			continue
		}
		if _, ok := c.Providers[providerName]; !ok {
			violations = append(violations, fmt.Errorf(
				"fallback_chain[%d] %q: provider %q does not match any entry in providers", i, entry, providerName))
		}
		// The model half is intentionally NOT validated against
		// providers.<name>.models or any live catalog: forge daemon
		// set-provider and /provider already proved a provider's real
		// catalog routinely has models never declared there (see
		// manual_usuario.md §11), and validating live here would mean
		// Config.Validate makes network calls, which it must not.
	}

	if strings.TrimSpace(c.Storage.Path) == "" {
		violations = append(violations, errors.New("storage.path must not be empty"))
	}

	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		violations = append(violations, fmt.Errorf(
			"logging.level %q is invalid (allowed: debug, info, warn, error)", c.Logging.Level))
	}

	for i, host := range c.Network.AllowedHosts {
		if strings.TrimSpace(host) == "" {
			violations = append(violations, fmt.Errorf(
				"network.allowed_hosts[%d]: entries must be non-empty host names", i))
		}
	}

	violations = append(violations, validatePermissions(c.Permissions)...)

	// Validate streaming mode: must be off|on|auto (default off is valid).
	switch strings.ToLower(strings.TrimSpace(c.LLM.Streaming.Mode)) {
	case "", StreamingModeOff, StreamingModeOn, StreamingModeAuto:
	default:
		violations = append(violations, fmt.Errorf(
			"llm.streaming.mode %q is invalid (allowed: %q, %q, %q)", c.LLM.Streaming.Mode, StreamingModeOff, StreamingModeOn, StreamingModeAuto))
	}
	// Normalize empty to off for callers that bypass Load.
	if strings.TrimSpace(c.LLM.Streaming.Mode) == "" {
		c.LLM.Streaming.Mode = StreamingModeOff
	} else {
		c.LLM.Streaming.Mode = strings.ToLower(strings.TrimSpace(c.LLM.Streaming.Mode))
	}

	// Validate cores (RNF-1.6): negative values are invalid; a value above
	// the machine's logical CPU count cannot be honored. Zero means
	// "default" (see DefaultInferenceCores) and is always valid; an
	// explicit value equal to NumCPU is allowed on purpose (user override).
	if c.LLM.Cores < 0 {
		violations = append(violations, fmt.Errorf(
			"llm.cores %d is invalid (must be >= 0; 0 selects the default budget)", c.LLM.Cores))
	} else if c.LLM.Cores > runtime.NumCPU() {
		violations = append(violations, fmt.Errorf(
			"llm.cores %d exceeds the machine's logical CPU count (%d)", c.LLM.Cores, runtime.NumCPU()))
	}

	if _, ok := normalizeSensitivity(c.Project.Sensitivity); !ok && strings.TrimSpace(c.Project.Sensitivity) != "" {
		violations = append(violations, fmt.Errorf(
			"project.sensitivity %q is invalid (allowed: %q, %q, %q — aliases: low, medium, high, regulated, sensitive)",
			c.Project.Sensitivity, SensitivityGeneral, SensitivityRegulated, SensitivitySensitive))
	} else if strings.TrimSpace(c.Project.Sensitivity) == "" {
		c.Project.Sensitivity = SensitivityGeneral
	} else if n, ok := normalizeSensitivity(c.Project.Sensitivity); ok {
		c.Project.Sensitivity = n
	}

	return errors.Join(violations...)
}

// normalizeSensitivity canonicalizes sensitivity aliases to the spec's Spanish
// values (RNF-9.1). Empty input is considered unknown; caller decides default.
func normalizeSensitivity(s string) (string, bool) {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	switch trimmed {
	case "", "general", "low":
		if trimmed == "" {
			return "", false
		}
		return SensitivityGeneral, true
	case "regulado", "regulated", "medium":
		return SensitivityRegulated, true
	case "datos-sensibles", "datos_sensibles", "datos sensibles", "sensitive", "high", "restricted":
		return SensitivitySensitive, true
	default:
		return "", false
	}
}

// SensitivityRank reports the maximum autonomy rank allowed for a given
// sensitivity level. Higher rank means more autonomy. Used to enforce
// RNF-9.2 ceiling: datos-sensibles hard-caps to supervised (1), regulado to
// checkpoint (2), general to autonomous (3). dry_run (0) is always allowed.
func SensitivityRank(s string) int {
	n, ok := normalizeSensitivity(s)
	if !ok {
		if strings.TrimSpace(s) == "" {
			n = SensitivityGeneral
		} else {
			return -1
		}
	}
	switch n {
	case SensitivitySensitive:
		return 1 // supervised
	case SensitivityRegulated:
		return 2 // checkpoint
	default:
		return 3 // autonomous (general)
	}
}

// AutonomyRank maps mode strings to increasing autonomy ranks.
// Unknown mode returns -1.
func AutonomyRank(mode string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "dry_run", "dry-run":
		return 0
	case "supervised":
		return 1
	case "checkpoint":
		return 2
	case "autonomous":
		return 3
	default:
		return -1
	}
}

// ValidateAutonomyAgainstSensitivity enforces RNF-9.3: a manifest requesting
// more autonomy than the project's sensitivity ceiling is rejected, never
// silently degraded. It also enforces the regulado pre-merge guard: regulado
// projects must declare a required pre-merge checkpoint.
func ValidateAutonomyAgainstSensitivity(mode, sensitivity string, hasPreMergeRequired bool) error {
	modeRank := AutonomyRank(mode)
	if modeRank < 0 {
		return fmt.Errorf("unknown autonomy mode %q", mode)
	}
	ceil := SensitivityRank(sensitivity)
	if ceil < 0 {
		return fmt.Errorf("unknown sensitivity %q", sensitivity)
	}
	if modeRank > ceil {
		return fmt.Errorf("autonomy mode %q exceeds project sensitivity ceiling %q (max rank %d, requested %d) — RNF-9.3", mode, sensitivity, ceil, modeRank)
	}
	if normalized, _ := normalizeSensitivity(sensitivity); normalized == SensitivityRegulated && modeRank >= 2 {
		if !hasPreMergeRequired {
			return fmt.Errorf("project sensitivity %q requires a checkpoint with trigger before_merge and required:true (RNF-9.2)", sensitivity)
		}
	}
	return nil
}

// validatePermissions checks the permissions section structurally. Glob
// syntax authority lives in internal/pathmatch (imported here so config and
// perms can never drift apart); this function only adds section context to
// every violation it reports.
func validatePermissions(p PermissionsPolicy) []error {
	var errs []error
	for i, pat := range p.FS.Read {
		if err := pathmatch.ValidatePattern(pat); err != nil {
			errs = append(errs, fmt.Errorf("permissions.fs.read[%d] %q: %v", i, pat, err))
		}
	}
	for i, pat := range p.FS.Write {
		if err := pathmatch.ValidatePattern(pat); err != nil {
			errs = append(errs, fmt.Errorf("permissions.fs.write[%d] %q: %v", i, pat, err))
		}
	}
	for i, entry := range p.Shell.Allow {
		if strings.TrimSpace(entry) == "" {
			errs = append(errs, fmt.Errorf(
				"permissions.shell.allow[%d]: entries must be non-empty command names", i))
			continue
		}
		if strings.Contains(entry, "*") || strings.Contains(entry, "?") || strings.Contains(entry, "[") {
			if _, err := filepath.Match(strings.ToLower(entry), "a"); err != nil {
				errs = append(errs, fmt.Errorf("permissions.shell.allow[%d] %q: %v", i, entry, err))
			}
		}
	}
	for i, entry := range p.Git.Allow {
		if strings.TrimSpace(entry) == "" {
			errs = append(errs, fmt.Errorf(
				"permissions.git.allow[%d]: entries must be non-empty subcommands", i))
		}
	}
	for i, entry := range p.Custom.Deny {
		if strings.TrimSpace(entry) == "" {
			errs = append(errs, fmt.Errorf(
				"permissions.custom.deny[%d]: entries must be non-empty tool names", i))
		}
	}
	for i, entry := range p.Custom.Allow {
		if strings.TrimSpace(entry) == "" {
			errs = append(errs, fmt.Errorf(
				"permissions.custom.allow[%d]: entries must be non-empty tool names", i))
		}
	}
	return errs
}

// Save writes c to path atomically: the marshaled document (two-space indent
// plus trailing newline) lands in a temp file inside the same directory,
// which is then renamed over path. Missing parent directories are created.
func (c *Config) Save(path string) error {
	path, err := ExpandPath(path)
	if err != nil {
		return fmt.Errorf("resolve config save path: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("set permissions on temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace config file %s: %w", path, err)
	}
	return nil
}
