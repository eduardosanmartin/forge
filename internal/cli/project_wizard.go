package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/run"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newWizardCommand())
}

func newWizardCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wizard <nombre-proyecto>",
		Short: "Interactive Q&A that fills SPEC.md, .forge/config.json and run.json with real content",
		Long: "Asks a short set of questions (descripción general, features, arquitectura\n" +
			"y stack, alcance, sensibilidad, proveedor de modelo) and writes SPEC.md,\n" +
			".forge/config.json y run.json a partir de las respuestas — a diferencia\n" +
			"de `forge init`, que escribe plantillas con placeholders \"TODO\".\n\n" +
			"Si <nombre-proyecto> no existe, lo crea. Si ya existe, pregunta si\n" +
			"continuar completándolo (puede sobrescribir los 3 archivos) o elegir\n" +
			"otro nombre — nunca sobrescribe en silencio.\n\n" +
			"No confundir con `forge plugin wizard` / `forge skill wizard`, que\n" +
			"scaffoldean un plugin o skill individual, no un proyecto completo.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := NewStdPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
			return runWizard(p, cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

// askList reads one item per line via p.Line until a blank line ends the
// list, built on the same Prompter used by `forge plugin wizard` / `forge
// skill wizard` (internal/cli/prompt.go) rather than a parallel input
// abstraction.
func askList(p Prompter, out io.Writer, label string) []string {
	fmt.Fprintf(out, "%s\n(un ítem por línea, línea vacía para terminar)\n", label)
	var items []string
	for {
		line := p.Line("  -", "")
		if line == "" {
			break
		}
		items = append(items, line)
	}
	return items
}

// chooseIndex prints a numbered menu and reads via p.Line, accepting either
// the option's 1-based number or its exact text. Kept separate from
// Prompter.Choose (which reprompts forever on an unrecognized answer) since
// a scripted/non-interactive wizard run must never block.
func chooseIndex(p Prompter, out io.Writer, label string, options []string, defIdx int) int {
	fmt.Fprintln(out, label)
	for i, opt := range options {
		marker := " "
		if i == defIdx {
			marker = "*"
		}
		fmt.Fprintf(out, "  %d%s) %s\n", i+1, marker, opt)
	}
	raw := p.Line(fmt.Sprintf("Elegí una opción (1-%d)", len(options)), strconv.Itoa(defIdx+1))
	if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n >= 1 && n <= len(options) {
		return n - 1
	}
	for i, opt := range options {
		if raw == opt {
			return i
		}
	}
	return defIdx
}

// wizardAnswers holds every answer the Q&A collects, kept separate from the
// config/run/spec builders below so those stay pure functions of data —
// easy to unit-test without driving a fake prompter for every case.
type wizardAnswers struct {
	Description   string
	Features      []string
	NonFunctional []string
	OutOfScope    []string
	Architecture  string
	Sensitivity   string
	ProviderKind  int // index into wizardProviderOptions
	BaseURL       string
	ModelName     string
	Goal          string
}

var wizardProviderOptions = []string{
	"Ollama local (http://127.0.0.1:11434/v1, sin cuenta externa)",
	"Proveedor remoto OpenAI-compatible (pedís base_url y modelo)",
	"Anthropic",
	"Gemini",
}

var wizardSensitivityOptions = []string{
	config.SensitivityGeneral,
	config.SensitivityRegulated,
	config.SensitivitySensitive,
}

func collectWizardAnswers(p Prompter, out io.Writer, projectName string) wizardAnswers {
	var a wizardAnswers

	a.Description = p.Line("Descripción general (1-3 oraciones: qué hace este proyecto y para quién)", "")
	a.Features = askList(p, out, "Features / funcionalidades principales")
	a.NonFunctional = askList(p, out, "Requisitos no funcionales (tests, performance, dependencias permitidas — opcional)")
	a.OutOfScope = askList(p, out, "Fuera de alcance explícito (qué NO construir en esta iteración — opcional)")
	a.Architecture = p.Line("Arquitectura y stack tecnológico (lenguaje, frameworks, estructura esperada)", "")

	sIdx := chooseIndex(p, out, "Sensibilidad del proyecto", []string{
		"general — sin datos regulados",
		"regulado — sujeto a compliance",
		"datos-sensibles — PII/secretos en juego",
	}, 0)
	a.Sensitivity = wizardSensitivityOptions[sIdx]

	a.ProviderKind = chooseIndex(p, out, "Proveedor de modelo a usar", wizardProviderOptions, 0)
	switch a.ProviderKind {
	case 1:
		a.BaseURL = p.Line("Base URL del proveedor OpenAI-compatible", "https://api.example.com/v1")
		a.ModelName = p.Line("Nombre del modelo", "gpt-4o-mini")
	case 2:
		a.ModelName = p.Line("Nombre del modelo Anthropic", "claude-3-5-sonnet-20241022")
	case 3:
		a.ModelName = p.Line("Nombre del modelo Gemini", "gemini-1.5-pro")
	default:
		a.ModelName = p.Line("Modelo servido por Ollama", "qwen2.5-coder:7b")
	}

	defGoal := "Implementar lo descrito en SPEC.md"
	if a.Description != "" {
		defGoal = a.Description
	}
	a.Goal = p.Line("Objetivo de la primera corrida (run.json \"goal\")", defGoal)

	return a
}

// resolveWizardDir implements the create-vs-existing-project gate the user
// required explicitly: a wizard run against an existing, non-empty project
// directory must ask for confirmation or a different name — it must never
// silently overwrite SPEC.md/.forge/config.json/run.json.
func resolveWizardDir(p Prompter, out io.Writer, name string) (string, error) {
	for {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("cancelado: nombre de proyecto vacío")
		}
		if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
			return "", &UsageError{Err: fmt.Errorf("el nombre %q no puede contener separadores de ruta ni \"..\"", name)}
		}

		entries, statErr := os.ReadDir(name)
		exists := statErr == nil && len(entries) > 0
		if !exists {
			return name, nil
		}

		fmt.Fprintf(out, "\nEl directorio %q ya existe y no está vacío.\n", name)
		choice := chooseIndex(p, out, "¿Qué querés hacer?", []string{
			"Continuar completando este proyecto existente",
			"Elegir otro nombre",
		}, 1)

		if choice != 0 {
			newName := p.Line("Nuevo nombre de proyecto", "")
			if newName == "" {
				return "", fmt.Errorf("cancelado por el usuario")
			}
			name = newName
			continue
		}

		confirmed := p.Bool(
			fmt.Sprintf("Esto puede sobrescribir SPEC.md, .forge/config.json y run.json en %q si ya existen. ¿Confirmás? (y/n)", name),
			false,
		)
		if confirmed {
			return name, nil
		}
		fmt.Fprintln(out, "No confirmado. Elegí otro nombre (o dejá vacío para cancelar).")
		newName := p.Line("Nuevo nombre de proyecto", "")
		if newName == "" {
			return "", fmt.Errorf("cancelado por el usuario")
		}
		name = newName
	}
}

func runWizard(p Prompter, out io.Writer, projectName string) error {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return &UsageError{Err: fmt.Errorf("project name must not be empty")}
	}

	dir, err := resolveWizardDir(p, out, projectName)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\n== Asistente de forge — %s ==\n", dir)
	fmt.Fprintln(out, "Respondé las preguntas (Enter acepta el valor por defecto entre [corchetes]).")

	ans := collectWizardAnswers(p, out, dir)

	forgeDir := filepath.Join(dir, ".forge")
	if err := os.MkdirAll(forgeDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", forgeDir, err)
	}

	slug := slugify(dir)

	cfg := wizardConfig(slug, ans)
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config.json: %w", err)
	}
	cfgPath := filepath.Join(forgeDir, "config.json")
	if err := os.WriteFile(cfgPath, append(cfgData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	manifest := wizardManifest(slug, ans)
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run.json: %w", err)
	}
	manifestPath := filepath.Join(dir, "run.json")
	if err := os.WriteFile(manifestPath, append(manifestData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifestPath, err)
	}

	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte(wizardSpecTemplate(dir, ans)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", specPath, err)
	}

	fmt.Fprintf(out, "\nProyecto completado en %s/\n", dir)
	fmt.Fprintf(out, "  %s\n  %s\n  %s\n\n", specPath, cfgPath, manifestPath)
	fmt.Fprintf(out, "Próximos pasos:\n")
	if ans.ProviderKind != 0 {
		fmt.Fprintf(out, "  1. Completá \"api_key\" en .forge/config.json (no se pide acá — no la guardes en un commit).\n")
	} else {
		fmt.Fprintf(out, "  1. Revisá .forge/config.json — Ollama local no necesita api_key.\n")
	}
	fmt.Fprintf(out, "  2. Repasá SPEC.md y run.json — podés editarlos a mano o correr `forge wizard %s` de nuevo.\n", dir)
	fmt.Fprintf(out, "  3. cd %s && ../forge.exe serve --addr 127.0.0.1:8765\n", dir)
	fmt.Fprintf(out, "  4. ../forge.exe run --manifest run.json\n")
	return nil
}

// wizardConfig starts from the same real-defaults baseline as `forge init`
// (initConfig) and overlays only what the Q&A actually determines:
// project.sensitivity and the provider block. Every non-answered section
// keeps forge's real default values, not zero-values — same rationale as
// initConfig's own doc comment.
func wizardConfig(slug string, a wizardAnswers) *config.Config {
	cfg := initConfig(slug)
	cfg.Project.Sensitivity = a.Sensitivity

	switch a.ProviderKind {
	case 1: // remote openai-compatible
		cfg.DefaultProvider = "remote"
		cfg.Providers = map[string]config.Provider{
			"remote": {
				Kind:    "openai-compatible",
				BaseURL: a.BaseURL,
				Models:  []string{a.ModelName},
				ModelRoles: map[string]string{
					"cheap":      a.ModelName,
					"generation": a.ModelName,
					"reasoning":  a.ModelName,
				},
				APIKey: "TODO: completá tu API key acá — no la commitees",
			},
		}
	case 2: // anthropic
		cfg.DefaultProvider = "anthropic"
		cfg.Providers = map[string]config.Provider{
			"anthropic": {
				Kind:    "anthropic",
				BaseURL: "https://api.anthropic.com",
				Models:  []string{a.ModelName},
				ModelRoles: map[string]string{
					"cheap":      a.ModelName,
					"generation": a.ModelName,
					"reasoning":  a.ModelName,
				},
				APIKey: "TODO: completá tu API key acá — no la commitees",
			},
		}
	case 3: // gemini
		cfg.DefaultProvider = "gemini"
		cfg.Providers = map[string]config.Provider{
			"gemini": {
				Kind:    "gemini",
				BaseURL: "https://generativelanguage.googleapis.com",
				Models:  []string{a.ModelName},
				ModelRoles: map[string]string{
					"cheap":      a.ModelName,
					"generation": a.ModelName,
					"reasoning":  a.ModelName,
				},
				APIKey: "TODO: completá tu API key acá — no la commitees",
			},
		}
	default: // ollama local — same shape initConfig already writes, just the chosen model
		cfg.Providers = map[string]config.Provider{
			"ollama": {
				Kind:    "openai-compatible",
				BaseURL: "http://127.0.0.1:11434/v1",
				Models:  []string{a.ModelName},
				ModelRoles: map[string]string{
					"cheap":      a.ModelName,
					"generation": a.ModelName,
					"reasoning":  a.ModelName,
				},
			},
		}
	}

	// Heuristic, best-effort shell allow-list from the free-text stack
	// answer: covers the common case without pretending to parse the
	// answer precisely — the user still reviews/edits this by hand.
	stack := strings.ToLower(a.Architecture)
	allow := []string{"git"}
	addIfMentioned := func(needle string, tools ...string) {
		if strings.Contains(stack, needle) {
			allow = append(allow, tools...)
		}
	}
	addIfMentioned("go", "go")
	addIfMentioned("node", "npm", "node")
	addIfMentioned("npm", "npm", "node")
	addIfMentioned("python", "python", "pip")
	addIfMentioned("rust", "cargo")
	addIfMentioned("java", "mvn", "gradle")
	if len(allow) == 1 {
		allow = append(allow, "go") // same fallback initConfig ships with
	}
	cfg.Permissions.Shell.Allow = dedupeStrings(allow)

	return cfg
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// wizardManifest reuses initManifest's real-usage-calibrated budget defaults
// and overlays only what the Q&A determined: run_id and goal.
func wizardManifest(slug string, a wizardAnswers) *run.Manifest {
	m := initManifest(slug)
	if a.Goal != "" {
		m.Goal = a.Goal
	}
	return m
}

func wizardSpecTemplate(projectName string, a wizardAnswers) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# SPEC — %s\n\n", projectName)
	b.WriteString("<!--\nGenerado por \"forge wizard\" a partir de las respuestas del Q&A.\nRevisá cada sección antes de correr \"forge run --manifest run.json\" — el\nasistente completa una estructura razonable, no reemplaza tu criterio.\nMantené la numeración RF-N/RNF-N (ver manual_usuario.md §18).\n-->\n\n")

	b.WriteString("## 1. Objetivo\n\n")
	if a.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", a.Description)
	} else {
		b.WriteString("TODO: 2-3 oraciones — qué hace este proyecto y para quién.\n\n")
	}

	b.WriteString("## 2. Requisitos funcionales\n\n")
	if len(a.Features) == 0 {
		b.WriteString("- **RF-1**: TODO\n\n")
	} else {
		for i, f := range a.Features {
			fmt.Fprintf(&b, "- **RF-%d**: %s\n", i+1, f)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 3. Requisitos no funcionales\n\n")
	if len(a.NonFunctional) == 0 {
		b.WriteString("- **RNF-1**: TODO — ej. cobertura de tests, dependencias permitidas, límites de performance.\n\n")
	} else {
		for i, r := range a.NonFunctional {
			fmt.Fprintf(&b, "- **RNF-%d**: %s\n", i+1, r)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 4. Fuera de alcance (explícito)\n\n")
	if len(a.OutOfScope) == 0 {
		b.WriteString("- TODO: listá explícitamente qué NO vas a construir en esta iteración.\n\n")
	} else {
		for _, o := range a.OutOfScope {
			fmt.Fprintf(&b, "- %s\n", o)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 5. Arquitectura y stack tecnológico\n\n")
	if a.Architecture != "" {
		fmt.Fprintf(&b, "%s\n\n", a.Architecture)
	} else {
		b.WriteString("TODO: lenguaje, frameworks, estructura de paquetes/carpetas esperada.\n\n")
	}

	b.WriteString("## 6. Referencias\n\n")
	b.WriteString("<!--\nOpcional. Rutas a mockups HTML, schemas, guías de estilo, etc. — el\nmodelo las lee con fs_read cuando una tarea las necesita (ver manual_usuario.md §10).\n-->\n- TODO (opcional)\n")

	return b.String()
}
