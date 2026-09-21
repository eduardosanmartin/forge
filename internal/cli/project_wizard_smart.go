package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eduardosanmartin/forge/internal/bootstrap"
	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

// wizardSmartMaxBlankInputs bounds how many consecutive empty command lines
// the main loop tolerates before aborting — a safety valve against hanging
// forever on a closed/non-interactive stdin (a scripted or piped
// invocation), since Prompter.Line can't distinguish "blank line" from EOF.
const wizardSmartMaxBlankInputs = 5

func newWizardSmartCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "smart <nombre-proyecto>",
		Short: "LLM-assisted loop: idea -> proposed RF/RNF -> SPEC.md, .forge/config.json, run.json",
		Long: "Reemplaza el formulario de `forge wizard` por un loop asistido por LLM\n" +
			"(hojaDeRuta-wizard-inteligente.md): describís la idea en 1-2 líneas, Forge\n" +
			"propone una lista de RF/RNF, y vos la curás — aceptar/descartar por número,\n" +
			"pedir más sugerencias, agregar las tuyas, o preguntar sobre un ítem puntual\n" +
			"antes de decidir — hasta que escribís \"listo\", momento en el que se generan\n" +
			"los 3 artefactos.\n\n" +
			"Requiere un daemon corriendo (`forge serve`) — la lógica del loop vive ahí,\n" +
			"no en este comando. No escribe nada a disco hasta \"listo\".",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := NewStdPrompter(cmd.InOrStdin(), cmd.OutOrStdout())
			return runWizardSmart(cmd.Context(), p, cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

func runWizardSmart(ctx context.Context, p Prompter, out io.Writer, projectName string) error {
	projectName = strings.TrimSpace(projectName)
	if projectName == "" {
		return &UsageError{Err: fmt.Errorf("project name must not be empty")}
	}

	// Same create-vs-existing-project gate as the classic wizard — checked
	// up front, before spending an idea+LLM conversation, not only at the
	// very end when writeSmartArtifacts would otherwise silently overwrite.
	dir, err := resolveWizardDir(p, out, projectName)
	if err != nil {
		return err
	}

	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	fmt.Fprintf(out, "\n== Asistente inteligente de forge — %s ==\n", dir)
	fmt.Fprintln(out, "Describí tu proyecto en 1-2 líneas:")
	idea := strings.TrimSpace(p.Line(">", ""))
	for idea == "" {
		idea = strings.TrimSpace(p.Line("La idea no puede estar vacía >", ""))
	}

	fmt.Fprintln(out, "\nAnalizando... (llamada al modelo)")
	var st bootstrap.State
	if err := cl.Call(ctx, daemon.MethodBootstrapStart, daemon.BootstrapStartParams{Idea: idea}, &st); err != nil {
		return fmt.Errorf("bootstrap.start: %w", err)
	}

	art, err := wizardSmartLoop(ctx, p, out, cl, &st)
	if err != nil {
		return err
	}
	if art == nil {
		fmt.Fprintln(out, "\nCancelado — no se escribió nada.")
		return nil
	}
	return writeSmartArtifacts(out, dir, art)
}

// wizardSmartLoop drives the selection loop (UX decisions #1-#5 in
// hojaDeRuta-wizard-inteligente.md) until the user says "listo" (returns
// the generated Artifacts) or "cancelar" (returns nil, nil).
func wizardSmartLoop(ctx context.Context, p Prompter, out io.Writer, cl *client.Client, st *bootstrap.State) (*bootstrap.Artifacts, error) {
	blanks := 0
	for {
		renderWizardSmartList(out, st)
		line := strings.TrimSpace(p.Line("Comando", ""))
		if line == "" {
			blanks++
			if blanks >= wizardSmartMaxBlankInputs {
				return nil, fmt.Errorf("demasiadas entradas vacías consecutivas, abortando")
			}
			continue
		}
		blanks = 0

		switch {
		case strings.EqualFold(line, "listo"):
			art, err := callBootstrapFinalize(ctx, cl, st.ID)
			if err != nil {
				fmt.Fprintf(out, "No se pudo generar los artefactos: %v\n", err)
				continue
			}
			return art, nil

		case strings.EqualFold(line, "cancelar"):
			return nil, nil

		case line == "+":
			fmt.Fprintln(out, "\nAnalizando... (llamada al modelo)")
			newSt, err := callBootstrapSuggestMore(ctx, cl, st.ID)
			if err != nil {
				fmt.Fprintf(out, "No se pudo pedir más sugerencias: %v\n", err)
				continue
			}
			st = newSt

		case strings.HasPrefix(line, "d ") || strings.HasPrefix(line, "d,"):
			idxs, perr := parseIndexList(strings.TrimSpace(line[1:]))
			if perr != nil {
				fmt.Fprintf(out, "No entendí los números a descartar: %v\n", perr)
				continue
			}
			newSt, err := callBootstrapDiscard(ctx, cl, st.ID, idxs)
			if err != nil {
				fmt.Fprintf(out, "No se pudo descartar: %v\n", err)
				continue
			}
			st = newSt

		case strings.HasPrefix(strings.ToLower(line), "sugerir:"):
			text := strings.TrimSpace(line[len("sugerir:"):])
			if text == "" {
				fmt.Fprintln(out, "La sugerencia no puede estar vacía.")
				continue
			}
			kindIdx := chooseIndex(p, out, "¿RF o RNF?", []string{"RF", "RNF"}, 0)
			kind := bootstrap.KindRF
			if kindIdx == 1 {
				kind = bootstrap.KindRNF
			}
			newSt, err := callBootstrapSuggestOwn(ctx, cl, st.ID, kind, text)
			if err != nil {
				fmt.Fprintf(out, "No se pudo agregar la sugerencia: %v\n", err)
				continue
			}
			st = newSt

		case strings.HasPrefix(line, "?"):
			idx, inline, perr := parseClarifyCommand(strings.TrimSpace(strings.TrimPrefix(line, "?")))
			if perr != nil {
				fmt.Fprintf(out, "No entendí a qué ítem te referís: %v\n", perr)
				continue
			}
			if cerr := wizardSmartClarifyLoop(ctx, p, out, cl, st, idx, inline); cerr != nil {
				fmt.Fprintf(out, "%v\n", cerr)
			}

		default:
			idxs, perr := parseIndexList(line)
			if perr != nil {
				fmt.Fprintf(out, "Comando no reconocido: %q\n", line)
				continue
			}
			newSt, err := callBootstrapSelect(ctx, cl, st.ID, idxs)
			if err != nil {
				fmt.Fprintf(out, "No se pudo aceptar: %v\n", err)
				continue
			}
			st = newSt
		}
	}
}

// wizardSmartClarifyLoop implements the "? N" sub-chat (UX decision #2):
// multi-turn, with an explicit "volver" back to the selection loop, never
// mutating st (bootstrap.clarify itself never touches Status/Index — see
// internal/bootstrap.Manager.Clarify's own doc comment).
func wizardSmartClarifyLoop(ctx context.Context, p Prompter, out io.Writer, cl *client.Client, st *bootstrap.State, idx int, inlineQuestion string) error {
	item, ok := findWizardSmartItem(st, idx)
	if !ok {
		return fmt.Errorf("no existe el ítem %d", idx)
	}
	fmt.Fprintf(out, "\n== Aclarando %s ==\n", item.ID)

	question := inlineQuestion
	for {
		if question == "" {
			fmt.Fprintln(out, "¿Qué querés saber?")
			question = strings.TrimSpace(p.Line(">", ""))
			if question == "" {
				fmt.Fprintln(out, "Volviendo a la selección.")
				return nil
			}
		}

		fmt.Fprintf(out, "\nAnalizando... (llamada al modelo, con la idea general + %s como contexto)\n", item.ID)
		var res daemon.BootstrapClarifyResult
		if err := cl.Call(ctx, daemon.MethodBootstrapClarify, daemon.BootstrapClarifyParams{
			BootstrapID: st.ID, Index: idx, Question: question,
		}, &res); err != nil {
			return fmt.Errorf("bootstrap.clarify: %w", err)
		}
		fmt.Fprintf(out, "\n%s\n", res.Answer)

		fmt.Fprintf(out, "\n¿Otra pregunta sobre %s, preguntar sobre otro ítem (\"? M\"), o volver a la selección?\n", item.ID)
		next := strings.TrimSpace(p.Line(">", ""))
		switch {
		case next == "" || strings.EqualFold(next, "volver"):
			return nil
		case strings.HasPrefix(next, "?"):
			newIdx, newInline, perr := parseClarifyCommand(strings.TrimSpace(strings.TrimPrefix(next, "?")))
			if perr != nil {
				fmt.Fprintf(out, "No entendí a qué ítem te referís: %v\n", perr)
				question = ""
				continue
			}
			newItem, ok := findWizardSmartItem(st, newIdx)
			if !ok {
				fmt.Fprintf(out, "No existe el ítem %d\n", newIdx)
				question = ""
				continue
			}
			idx, item = newIdx, newItem
			fmt.Fprintf(out, "\n== Aclarando %s ==\n", item.ID)
			question = newInline
		default:
			question = next
		}
	}
}

func findWizardSmartItem(st *bootstrap.State, idx int) (bootstrap.Item, bool) {
	for _, it := range st.Items {
		if it.Index == idx {
			return it, true
		}
	}
	return bootstrap.Item{}, false
}

// parseIndexList parses a comma-separated list of item indices (e.g.
// "1, 2,3"). Used for both the plain accept form and "d N,M"'s tail.
func parseIndexList(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q no es un número", part)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("lista vacía")
	}
	return out, nil
}

// parseClarifyCommand splits "? N" or "? N <pregunta>" (rest already has
// the leading "?" stripped) into the item index and, when present, the
// inline question — the roadmap's "resuelto" note: both the two-step and
// one-line forms of "? N" are supported, never mutually exclusive.
func parseClarifyCommand(rest string) (index int, inlineQuestion string, err error) {
	fields := strings.SplitN(rest, " ", 2)
	if fields[0] == "" {
		return 0, "", fmt.Errorf(`uso: "? N" o "? N pregunta"`)
	}
	idx, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", fmt.Errorf("%q no es un número de ítem", fields[0])
	}
	if len(fields) == 2 {
		inlineQuestion = strings.TrimSpace(fields[1])
	}
	return idx, inlineQuestion, nil
}

// renderWizardSmartList prints the RF/RNF sections and the command legend —
// re-rendered after every command, always the same numbering (UX decision
// #1), with a status suffix only on a non-pending item so the pending case
// stays visually identical to the roadmap's mockup.
func renderWizardSmartList(out io.Writer, st *bootstrap.State) {
	fmt.Fprintln(out, "\n== RF propuestos ==")
	printWizardSmartItems(out, st, bootstrap.KindRF)
	fmt.Fprintln(out, "\n== RNF propuestos ==")
	printWizardSmartItems(out, st, bootstrap.KindRNF)
	fmt.Fprintln(out)
	fmt.Fprint(out, `Comandos: números separados por coma para ACEPTAR (ej. "1,2,3,6,7")
          "d 4,5"        para descartar
          "+"            para pedir más sugerencias
          "sugerir: ..." para agregar una propia
          "? 5"          para preguntar sobre un ítem antes de decidir
          "listo"        cuando el conjunto te parezca sólido
          "cancelar"     para salir sin generar nada
`)
}

func printWizardSmartItems(out io.Writer, st *bootstrap.State, kind bootstrap.Kind) {
	anyItem := false
	for _, it := range st.Items {
		if it.Kind != kind {
			continue
		}
		anyItem = true
		suffix := ""
		switch it.Status {
		case bootstrap.StatusAccepted:
			suffix = "  (aceptado)"
		case bootstrap.StatusDiscarded:
			suffix = "  (descartado)"
		}
		fmt.Fprintf(out, "  [%d] %s  %s%s\n", it.Index, it.ID, it.Text, suffix)
	}
	if !anyItem {
		fmt.Fprintln(out, "  (ninguno)")
	}
}

func callBootstrapSelect(ctx context.Context, cl *client.Client, id string, indices []int) (*bootstrap.State, error) {
	var st bootstrap.State
	if err := cl.Call(ctx, daemon.MethodBootstrapSelect, daemon.BootstrapSelectParams{BootstrapID: id, Indices: indices}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func callBootstrapDiscard(ctx context.Context, cl *client.Client, id string, indices []int) (*bootstrap.State, error) {
	var st bootstrap.State
	if err := cl.Call(ctx, daemon.MethodBootstrapDiscard, daemon.BootstrapDiscardParams{BootstrapID: id, Indices: indices}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func callBootstrapSuggestMore(ctx context.Context, cl *client.Client, id string) (*bootstrap.State, error) {
	var st bootstrap.State
	if err := cl.Call(ctx, daemon.MethodBootstrapSuggestMore, daemon.BootstrapSuggestMoreParams{BootstrapID: id}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func callBootstrapSuggestOwn(ctx context.Context, cl *client.Client, id string, kind bootstrap.Kind, text string) (*bootstrap.State, error) {
	var st bootstrap.State
	if err := cl.Call(ctx, daemon.MethodBootstrapSuggestOwn, daemon.BootstrapSuggestOwnParams{BootstrapID: id, Kind: string(kind), Text: text}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func callBootstrapFinalize(ctx context.Context, cl *client.Client, id string) (*bootstrap.Artifacts, error) {
	var art bootstrap.Artifacts
	if err := cl.Call(ctx, daemon.MethodBootstrapFinalize, daemon.BootstrapFinalizeParams{BootstrapID: id}, &art); err != nil {
		return nil, err
	}
	return &art, nil
}

// writeSmartArtifacts writes Finalize's three artifacts to dir — the only
// place this whole command touches the filesystem, matching Fase 0's
// architecture decision that the daemon never does.
func writeSmartArtifacts(out io.Writer, dir string, art *bootstrap.Artifacts) error {
	forgeDir := filepath.Join(dir, ".forge")
	if err := os.MkdirAll(forgeDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", forgeDir, err)
	}

	cfgData, err := json.MarshalIndent(art.Config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config.json: %w", err)
	}
	cfgPath := filepath.Join(forgeDir, "config.json")
	if err := os.WriteFile(cfgPath, append(cfgData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	// Finalize derived RunID from the idea text (it doesn't know the real
	// project directory name — Fase 0's daemon-has-no-filesystem rule).
	// Now that we do, override it with the same slug+"-01" convention the
	// classic wizard's own RunID uses.
	art.Manifest.RunID = slugify(dir) + "-01"
	manifestData, err := json.MarshalIndent(art.Manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run.json: %w", err)
	}
	manifestPath := filepath.Join(dir, "run.json")
	if err := os.WriteFile(manifestPath, append(manifestData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", manifestPath, err)
	}

	specPath := filepath.Join(dir, "SPEC.md")
	if err := os.WriteFile(specPath, []byte(art.SpecMD), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", specPath, err)
	}

	fmt.Fprintf(out, "\nProyecto completado en %s/\n", dir)
	fmt.Fprintf(out, "  %s\n  %s\n  %s\n\n", specPath, cfgPath, manifestPath)
	fmt.Fprintf(out, "Próximos pasos:\n")
	fmt.Fprintf(out, "  1. Revisá .forge/config.json — proveedor por defecto: Ollama local. Cambialo si corresponde.\n")
	fmt.Fprintf(out, "  2. Repasá SPEC.md y run.json — el modelo propuso las tareas desglosadas, revisalas antes de correr.\n")
	fmt.Fprintf(out, "  3. cd %s && ../forge.exe serve --addr 127.0.0.1:8765\n", dir)
	fmt.Fprintf(out, "  4. ../forge.exe run --manifest run.json\n")
	return nil
}
