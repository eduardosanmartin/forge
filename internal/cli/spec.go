package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newSpecCommand())
}

// Trust boundary: forge spec shells out to git ONLY for read-only subcommands
// (log, show, diff). Mutating git subcommands are never allowed here. The
// permission engine leashes MODEL tool calls; the CLI is the owner acting
// directly — the same rationale documented for the memory.* RPCs in
// handler_memory.go. Specs are project artifacts the owner tracks in git; the
// CLI merely surfaces their existing history.

// specProbeNames lists the candidate spec file names probed in the workspace
// root when neither --path nor config project.spec_path apply.
var specProbeNames = []string{"spec-harness-agentic.md", "SPEC.md", "spec.md"}

// ResolveSpecPath resolves the effective spec file path:
// explicit flag > config project.spec_path > probe of specProbeNames in
// workspaceRoot. Returns the workspace-relative path when probing, or the
// given path otherwise. Error lists all probed names when nothing is found.
func ResolveSpecPath(workspaceRoot, flagPath, configSpecPath string) (string, error) {
	if p := strings.TrimSpace(flagPath); p != "" {
		return p, nil
	}
	if p := strings.TrimSpace(configSpecPath); p != "" {
		return p, nil
	}
	for _, name := range specProbeNames {
		if _, err := os.Stat(filepath.Join(workspaceRoot, name)); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("spec file not found: provide --path, set project.spec_path in config, or place one of %q in the workspace root", specProbeNames)
}

func newSpecCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Short: "Git-native spec versioning (RF-8.4)",
		Long:  "Surface the version history of the spec file already tracked in git. Read-only: git is invoked with log/show/diff only. Every subcommand supports --json (one RF-6.3 envelope per invocation).",
	}
	cmd.AddCommand(newSpecShowCommand())
	cmd.AddCommand(newSpecLogCommand())
	cmd.AddCommand(newSpecDiffCommand())
	return cmd
}

func newSpecShowCommand() *cobra.Command {
	var jsonOut bool
	var path string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the current spec file content",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpecShow(cmd, cmd.OutOrStdout(), path, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().StringVar(&path, "path", "", "spec file path (default: project.spec_path config, then probe)")
	return cmd
}

func newSpecLogCommand() *cobra.Command {
	var jsonOut bool
	var path string
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Git commit history touching the spec file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpecLog(cmd, cmd.OutOrStdout(), path, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().StringVar(&path, "path", "", "spec file path (default: project.spec_path config, then probe)")
	return cmd
}

func newSpecDiffCommand() *cobra.Command {
	var jsonOut bool
	var path string
	cmd := &cobra.Command{
		Use:   "diff <ref> [<ref>]",
		Short: "Diff the spec file (working tree vs ref, or between two refs)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSpecDiff(cmd, cmd.OutOrStdout(), args, path, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON envelope on stdout")
	cmd.Flags().StringVar(&path, "path", "", "spec file path (default: project.spec_path config, then probe)")
	return cmd
}

// specConfigExtracts encapsulates RunE dependencies: config spec path and
// workspace root (cwd, consistent with the rest of the CLI).
func specInvocation(cmd *cobra.Command) (root, cfgSpecPath string, err error) {
	app, ok := AppFromContext(cmd.Context())
	if ok && app.Config != nil {
		cfgSpecPath = app.Config.Project.SpecPath
	}
	root, err = os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace root: %w", err)
	}
	return root, cfgSpecPath, nil
}

type specShowResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func runSpecShow(cmd *cobra.Command, out io.Writer, flagPath string, jsonOut bool) error {
	const command = "spec show"
	root, cfgSpecPath, err := specInvocation(cmd)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	specPath, err := ResolveSpecPath(root, flagPath, cfgSpecPath)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	content, err := runSpecFileShow(root, specPath)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	res := specShowResult{Path: specPath, Content: content}
	if jsonOut {
		return writeJSONResultEnvelope(out, command, res)
	}
	fmt.Fprint(out, content)
	return nil
}

type specLogEntry struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Author    string `json:"author"`
	Date      string `json:"date"`
	Subject   string `json:"subject"`
}

type specLogResult struct {
	Path    string         `json:"path"`
	Entries []specLogEntry `json:"entries"`
}

func runSpecLog(cmd *cobra.Command, out io.Writer, flagPath string, jsonOut bool) error {
	const command = "spec log"
	root, cfgSpecPath, err := specInvocation(cmd)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	specPath, err := ResolveSpecPath(root, flagPath, cfgSpecPath)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	entries, err := runSpecFileLog(root, specPath)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	res := specLogResult{Path: specPath, Entries: entries}
	if jsonOut {
		return writeJSONResultEnvelope(out, command, res)
	}
	for _, e := range entries {
		fmt.Fprintf(out, "%s %s %s %s\n", e.ShortHash, e.Date, e.Author, e.Subject)
	}
	return nil
}

type specDiffResult struct {
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to,omitempty"`
	Diff string `json:"diff"`
}

func runSpecDiff(cmd *cobra.Command, out io.Writer, args []string, flagPath string, jsonOut bool) error {
	const command = "spec diff"
	root, cfgSpecPath, err := specInvocation(cmd)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	specPath, err := ResolveSpecPath(root, flagPath, cfgSpecPath)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	from, to := args[0], ""
	if len(args) > 1 {
		to = args[1]
	}
	diff, err := runSpecFileDiff(root, specPath, from, to)
	if err != nil {
		return envelopeErr(out, command, jsonOut, err)
	}
	res := specDiffResult{Path: specPath, From: from, To: to, Diff: diff}
	if jsonOut {
		return writeJSONResultEnvelope(out, command, res)
	}
	fmt.Fprint(out, diff)
	return nil
}

// envelopeErr either surfaces err directly or renders it as the RF-6.3 error
// envelope when --json was requested.
func envelopeErr(out io.Writer, command string, jsonOut bool, err error) error {
	if jsonOut {
		_ = writeJSONErrorEnvelope(out, command, err.Error())
	}
	return err
}

// runSpecFileShow reads the spec file from disk. Kept beside the git helpers
// because `forge spec show` is intentionally a plain file read (the resolved
// path), not a git blob lookup.
func runSpecFileShow(root, specPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, specPath))
	if err != nil {
		return "", fmt.Errorf("read spec file %q: %w", specPath, err)
	}
	return string(data), nil
}

// runGitRepo verifies that root is inside a git work tree and that the git
// binary is available, so spec subcommands fail with a clean message instead
// of leaking git stderr for the common cases.
func runGitRepo(root string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git not found on PATH: %w", err)
	}
	cmd := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "not a git repository") {
			return fmt.Errorf("not a git repository: %s", root)
		}
		return fmt.Errorf("git rev-parse failed: %s", strings.TrimSpace(stderr.String()))
	}
	if strings.TrimSpace(string(out)) != "true" {
		return fmt.Errorf("not a git repository: %s", root)
	}
	return nil
}

// runGit runs one read-only git subcommand in root and returns stdout.
// Non-zero exit is surfaced as an error carrying the trimmed stderr detail.
// Only log/show/diff-style subcommands may be passed here; see the trust
// boundary note at the top of this file.
func runGit(root string, args ...string) (string, error) {
	if err := runGitRepo(root); err != nil {
		return "", err
	}
	full := append([]string{"-C", root}, args...)
	cmd := exec.Command("git", full...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = "git exited with an error"
		}
		return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), detail)
	}
	return string(out), nil
}

const specLogFormat = "%H%x1f%h%x1f%an%x1f%aI%x1f%s%x1e"

// runSpecFileLog returns the commit history touching specPath, newest first
// (git log --follow so renames of the spec are tracked).
func runSpecFileLog(root, specPath string) ([]specLogEntry, error) {
	out, err := runGit(root, "log", "--follow", "--format="+specLogFormat, "--", specPath)
	if err != nil {
		return nil, err
	}
	entries := []specLogEntry{}
	for _, rec := range strings.Split(strings.TrimSuffix(out, "\n"), "\x1e") {
		rec = strings.TrimPrefix(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.Split(rec, "\x1f")
		if len(parts) != 5 {
			return nil, fmt.Errorf("malformed git log record for path %q", specPath)
		}
		entries = append(entries, specLogEntry{
			Hash: parts[0], ShortHash: parts[1], Author: parts[2],
			Date: parts[3], Subject: parts[4],
		})
	}
	return entries, nil
}

// runSpecFileDiff returns the git diff for specPath. One ref diffs the working
// tree against that ref; two refs diff between them.
func runSpecFileDiff(root, specPath, from, to string) (string, error) {
	args := []string{"diff"}
	if to == "" {
		args = append(args, from)
	} else {
		args = append(args, from, to)
	}
	args = append(args, "--", specPath)
	return runGit(root, args...)
}
