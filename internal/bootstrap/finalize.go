package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/run"
)

// Artifacts is Finalize's coordinated output: the three files the
// roadmap's Objetivo requires, as content only — never written to disk
// here (see the package doc comment).
type Artifacts struct {
	SpecMD   string         `json:"spec_md"`
	Config   *config.Config `json:"config"`
	Manifest *run.Manifest  `json:"manifest"`
}

// Finalize is the "listo" command: builds SPEC.md deterministically from
// every ACCEPTED item (no LLM call needed — the text is already there),
// decomposes it into run.json's Tasks via the same Decomposer
// run.start's own --decompose flag uses (see WithDecomposer), and pairs
// it with a safe-defaults .forge/config.json the user edits afterward
// (provider/sensitivity aren't part of this flow's Q&A, unlike `forge
// wizard`'s). Marks the session Finalized on success.
func (m *Manager) Finalize(ctx context.Context, id string) (*Artifacts, error) {
	if m.decompose == nil {
		return nil, fmt.Errorf("bootstrap finalize decomposition is not configured")
	}
	st, err := m.getSession(id)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	items := append([]Item(nil), st.Items...)
	idea := st.Idea
	m.mu.Unlock()

	acceptedCount := 0
	for _, it := range items {
		if it.Status == StatusAccepted {
			acceptedCount++
		}
	}
	if acceptedCount == 0 {
		return nil, fmt.Errorf("%w: no accepted items to finalize — select at least one RF or RNF first", ErrInvalidRequest)
	}

	specMD := buildSpecMD(idea, items)
	goal := strings.TrimSpace(idea)
	if goal == "" {
		goal = "Implementar lo descrito en SPEC.md"
	}

	tasks, err := m.decompose(ctx, goal, specMD)
	if err != nil {
		return nil, fmt.Errorf("decompose: %w", err)
	}

	// Budget/Git/HITL defaults mirror internal/cli/init.go's initManifest —
	// the same real-usage-calibrated numbers, duplicated rather than
	// imported because internal/cli depends on internal/daemon/bootstrap,
	// not the other way around (Fase 0's daemon/CLI layering).
	manifest := &run.Manifest{
		RunID:   slugFromIdea(idea) + "-01",
		Mode:    "checkpoint",
		Goal:    goal,
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
		Tasks: tasks,
	}

	m.mu.Lock()
	st.Finalized = true
	m.mu.Unlock()

	return &Artifacts{
		SpecMD:   specMD,
		Config:   config.Defaults(),
		Manifest: manifest,
	}, nil
}

// buildSpecMD renders SPEC.md from every ACCEPTED item, grouped RF then
// RNF, in their existing Index order — same "- **RF-N**: text" format
// `forge wizard`'s own SPEC template uses, so both tools produce specs a
// human reads the same way.
func buildSpecMD(idea string, items []Item) string {
	var b strings.Builder
	b.WriteString("# SPEC\n\n")
	b.WriteString("<!--\nGenerado por el wizard inteligente de forge (bootstrap.finalize) a partir\nde una idea y de los RF/RNF aceptados durante el loop. Revisá cada\nsección antes de correr \"forge run --manifest run.json\". Mantené la\nnumeración RF-N/RNF-N (ver manual_usuario.md §18).\n-->\n\n")

	b.WriteString("## 1. Objetivo\n\n")
	b.WriteString(strings.TrimSpace(idea))
	b.WriteString("\n\n")

	b.WriteString("## 2. Requisitos funcionales\n\n")
	anyRF := false
	for _, it := range items {
		if it.Kind == KindRF && it.Status == StatusAccepted {
			fmt.Fprintf(&b, "- **%s**: %s\n", it.ID, it.Text)
			anyRF = true
		}
	}
	if !anyRF {
		b.WriteString("- **RF-1**: TODO\n")
	}

	b.WriteString("\n## 3. Requisitos no funcionales\n\n")
	anyRNF := false
	for _, it := range items {
		if it.Kind == KindRNF && it.Status == StatusAccepted {
			fmt.Fprintf(&b, "- **%s**: %s\n", it.ID, it.Text)
			anyRNF = true
		}
	}
	if !anyRNF {
		b.WriteString("- **RNF-1**: TODO\n")
	}
	b.WriteString("\n")

	return b.String()
}

// slugFromIdea derives a short, filesystem-safe slug from the idea text
// for Manifest.RunID (e.g. "run-id-01") — a best-effort default the CLI
// (Fase 5) is free to override once it knows the real project directory
// name, same as `forge wizard`'s own slugify does for its own RunID.
func slugFromIdea(idea string) string {
	idea = strings.ToLower(strings.TrimSpace(idea))
	var b strings.Builder
	prevDash := false
	for _, r := range idea {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = "proyecto"
	}
	return s
}
