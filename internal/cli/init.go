package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/run"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newInitCommand())
}

func newInitCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init <nombre-proyecto>",
		Short: "Scaffold a new project (SPEC.md, .forge/config.json, run.json)",
		Long: "Creates <nombre-proyecto>/ with three documented starter files: SPEC.md\n" +
			"(RF/RNF template), .forge/config.json (valid, loadable config with\n" +
			"placeholder provider credentials), and run.json (a minimal, valid manifest\n" +
			"with an empty task list — runs as a single task from \"goal\" as-is, or\n" +
			"ready for `forge run --manifest run.json --decompose`).\n\n" +
			"Refuses to scaffold into an existing non-empty directory unless --force.\n" +
			"Purely static templates — for an interactive Q&A that fills these in with\n" +
			"real project content instead of placeholders, see `forge wizard`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd.OutOrStdout(), args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "scaffold into an existing non-empty directory anyway")
	return cmd
}

// slugRe matches characters safe for a run_id and a storage-path filename
// component: lowercase letters, digits, and hyphens only.
var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// slugify derives a run_id/filename-safe slug from a project name: lowercase,
// spaces/underscores collapsed to a single hyphen, anything else stripped.
// Never empty — falls back to "project" so a name that's ALL punctuation
// (rare, but not worth failing init over) still produces a valid run_id.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.Join(strings.Fields(s), "-")
	s = slugRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		return "project"
	}
	return s
}

func runInit(out io.Writer, projectName string, force bool) error {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return &UsageError{Err: fmt.Errorf("project name must not be empty")}
	}
	if strings.ContainsAny(projectName, `/\`) || strings.Contains(projectName, "..") {
		return &UsageError{Err: fmt.Errorf("project name %q must not contain path separators or \"..\" — it names a new directory in the current one", projectName)}
	}

	dir := projectName
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 && !force {
		return fmt.Errorf("directory %q already exists and is not empty — pick another name, remove it yourself, or pass --force to scaffold into it anyway", dir)
	}

	forgeDir := filepath.Join(dir, ".forge")
	if err := os.MkdirAll(forgeDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", forgeDir, err)
	}

	slug := slugify(projectName)

	cfg := initConfig(slug)
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config.json: %w", err)
	}
	cfgPath := filepath.Join(forgeDir, "config.json")
	if err := os.WriteFile(cfgPath, append(cfgData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	manifest := initManifest(slug)
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run.json: %w", err)
	}
	manifestPath := filepath.Join(dir, "run.json")
	if err := os.WriteFile(manifestPath, append(manifestData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifestPath, err)
	}

	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte(initSpecTemplate(projectName)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", specPath, err)
	}

	fmt.Fprintf(out, "Proyecto scaffoldeado en %s/\n", dir)
	fmt.Fprintf(out, "  %s\n  %s\n  %s\n\n", specPath, cfgPath, manifestPath)
	fmt.Fprintf(out, "Próximos pasos:\n")
	fmt.Fprintf(out, "  1. Completá SPEC.md (objetivo, RF-N/RNF-N, fuera de alcance).\n")
	fmt.Fprintf(out, "  2. Revisá .forge/config.json — completá \"api_key\" si tu proveedor lo pide,\n")
	fmt.Fprintf(out, "     y ajustá \"providers\"/\"model_roles\" a lo que realmente vayas a usar.\n")
	fmt.Fprintf(out, "  3. Completá \"goal\" en run.json, y \"tasks\" a mano o con --decompose.\n")
	fmt.Fprintf(out, "  4. cd %s && ../forge.exe serve --addr 127.0.0.1:8765\n", dir)
	fmt.Fprintf(out, "  5. ../forge.exe run --manifest run.json\n")
	return nil
}

// scaffoldAgentMaxIterations overrides config.DefaultAgentMaxIterations (10)
// for generated projects. 10 is a per-TURN tool-call cap, not the run-wide
// budget — observed in practice (a real single-task manifest run) a task
// doing little more than a handful of fs_read/fs_write calls burned all 10
// slots and hit "agent reached max_iterations" before it could finish,
// forcing an avoidable retry. 30 still leaves 10x headroom under the
// scaffolded manifest's own run-wide budget.max_iterations (300 in
// initManifest) while still catching a genuinely runaway turn.
const scaffoldAgentMaxIterations = 30

// initConfig builds a valid, loadable starter config: real default
// constants where forge already has one (never hand-copied magic numbers,
// so this can't silently drift from the library's own defaults), an
// explicit local Ollama provider (no external account needed to try it),
// and project.spec_path wired to the SPEC.md this same command writes.
func initConfig(slug string) *config.Config {
	return &config.Config{
		SchemaVersion:   config.CurrentSchemaVersion,
		DefaultProvider: "ollama",
		Providers: map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: "http://127.0.0.1:11434/v1",
				Models:  []string{"qwen2.5-coder:7b"},
				ModelRoles: map[string]string{
					"cheap":      "qwen2.5-coder:7b",
					"generation": "qwen2.5-coder:7b",
					"reasoning":  "qwen2.5-coder:7b",
				},
			},
		},
		Storage: config.StorageConfig{Path: "~/.forge/" + slug + ".db"},
		Network: config.NetworkConfig{AllowedHosts: []string{"127.0.0.1", "localhost"}},
		Logging: config.LoggingConfig{Level: "info"},
		TUI:     config.TUIConfig{Layout: "hybrid", Palette: "ember", Sidebar: true},
		LLM:     config.LLMConfig{Streaming: config.StreamingConfig{Mode: config.StreamingModeOff}},
		Limits: config.LimitsConfig{
			PluginWasmMaxBytes: config.DefaultPluginWasmMaxBytes,
			SkillFileMaxBytes:  config.DefaultSkillFileMaxBytes,
		},
		Permissions: config.PermissionsPolicy{
			FS:    config.FSPermissions{Read: []string{"./**"}, Write: []string{"./**"}},
			Shell: config.ShellPermissions{Allow: []string{"go", "git"}, RequireIsolation: true},
			Git: config.GitPermissions{Allow: []string{
				"status", "add", "commit", "log", "diff", "branch",
				"switch", "stash", "restore", "show",
			}},
			// Empty, non-nil slices: renders as "[]" in the generated JSON
			// (deny-by-default, nothing granted yet) instead of "null" —
			// clearer to a human about to fill these in by hand.
			GitHub: config.GitHubPermissions{Allow: []string{}},
			Custom: config.CustomPermissions{Deny: []string{}, Allow: []string{}},
		},
		Agent: config.AgentConfig{
			MaxIterations:       scaffoldAgentMaxIterations,
			MaxTurnSeconds:      config.DefaultAgentMaxTurnSeconds,
			MaxParallelChildren: config.DefaultAgentMaxParallelChildren,
		},
		Project: config.ProjectConfig{
			Sensitivity: config.SensitivityGeneral,
			SpecPath:    "SPEC.md",
		},
	}
}

// initManifest builds a minimal, VALID (Manifest.Validate passes) starter
// manifest with no tasks: EffectiveTasks() falls back to a single task from
// "goal" as-is, and it's already shaped correctly for
// `forge run --manifest run.json --decompose` (requires empty "tasks").
// Budget numbers are not the library's small defaults — they reflect real
// usage measured running two example manifests to completion this session
// (chores-cli: ~45K tokens / ~90 iterations for 3 tasks), with real margin
// rather than a guess that forces an early restart.
func initManifest(slug string) *run.Manifest {
	return &run.Manifest{
		RunID:   slug + "-01",
		Mode:    "checkpoint",
		Goal:    "TODO: objetivo de esta corrida, en una oración.",
		SpecRef: "SPEC.md",
		Budget: run.Budget{
			MaxWallClock:      "60m",
			MaxTokens:         150000,
			MaxIterations:     300,
			MaxRetriesPerTask: 2,
		},
		Git: run.GitConfig{
			Isolation:     "worktree",
			CommitPerTask: true,
		},
		HITL: run.HITLConfig{
			Checkpoints: []run.Checkpoint{
				{ID: "cp-before-merge", Trigger: run.TriggerBeforeMerge, Required: true},
			},
		},
		Tasks: []run.Task{},
	}
}

func initSpecTemplate(projectName string) string {
	return fmt.Sprintf(`# SPEC — %s

<!--
Plantilla generada por "forge init". Reemplazá cada sección con contenido
real antes de correr "forge run --manifest run.json". Mantené la
numeración RF-N/RNF-N: es la convención que usa forge para que
"forge spec validate"/"spec log" puedan rastrear evidencia de
implementación por requisito (ver manual_usuario.md §18).
-->

## 1. Objetivo

TODO: 2-3 oraciones — qué hace este proyecto y para quién.

## 2. Requisitos funcionales

- **RF-1**: TODO
- **RF-2**: TODO

## 3. Requisitos no funcionales

- **RNF-1**: TODO — ej. cobertura de tests, dependencias permitidas, límites de performance.

## 4. Fuera de alcance (explícito)

- TODO: listá explícitamente qué NO vas a construir en esta iteración —
  evita que el agente "agregue valor" fuera de lo pedido.

## 5. Arquitectura y stack tecnológico

TODO: lenguaje, frameworks, estructura de paquetes/carpetas esperada.

## 6. Referencias

<!--
Opcional. Rutas a mockups HTML, schemas, guías de estilo, etc. — el
modelo las lee con fs_read cuando una tarea las necesita; no hace falta
cargarlas todas de antemano en spec_ref (ver manual_usuario.md §10).
-->
- TODO (opcional)
`, projectName)
}
