package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/logging"
	"github.com/eduardosanmartin/forge/internal/mining"
	"github.com/eduardosanmartin/forge/internal/skill"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newSkillCommand())
}

func newSkillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage forge skills",
	}
	cmd.AddCommand(newSkillNewCommand())
	cmd.AddCommand(newSkillValidateCommand())
	cmd.AddCommand(newSkillInstallCommand())
	cmd.AddCommand(newSkillListCommand())
	cmd.AddCommand(newSkillEnableCommand())
	cmd.AddCommand(newSkillDisableCommand())
	cmd.AddCommand(newSkillRemoveCommand())
	cmd.AddCommand(newSkillMineCommand())
	return cmd
}

func newSkillNewCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create a new skill scaffold (interactive)",
		RunE: func(cmd *cobra.Command, args []string) error {
			prompter := NewStdPrompter(os.Stdin, os.Stdout)
			root := filepath.Join(".", ".forge", "skills")
			return runSkillWizard(prompter, os.Stdout, root, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing directory")
	return cmd
}

var skillNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)

func runSkillWizard(p Prompter, out io.Writer, skillsRoot string, force bool) error {
	var name string
	for {
		raw := p.Line("Skill name (e.g. my-skill)", "")
		if raw == "" {
			fmt.Fprintln(out, "name is required")
			continue
		}
		if !skillNameRe.MatchString(raw) {
			fmt.Fprintf(out, "invalid name %q: must match ^[a-z][a-z0-9_-]{1,63}$\n", raw)
			continue
		}
		name = raw
		break
	}
	description := p.Line("Description", fmt.Sprintf("%s skill", name))
	if strings.TrimSpace(description) == "" {
		description = fmt.Sprintf("%s skill", name)
	}
	category := p.Line("Category", "")
	keywordsRaw := p.Line("Activation keywords (comma-separated)", "")
	var keywords []string
	if strings.TrimSpace(keywordsRaw) != "" {
		parts := strings.Split(keywordsRaw, ",")
		for _, part := range parts {
			kw := strings.TrimSpace(part)
			if kw != "" {
				keywords = append(keywords, kw)
			}
		}
	}
	scriptsRaw := p.Line("Scripts (comma-separated filenames, e.g. scripts/check.sh)", "")
	var scripts []string
	if strings.TrimSpace(scriptsRaw) != "" {
		parts := strings.Split(scriptsRaw, ",")
		for _, part := range parts {
			s := strings.TrimSpace(part)
			if s != "" {
				scripts = append(scripts, s)
			}
		}
	}
	source := p.Choose("Source", []string{"local", "external"}, "local")
	if source != "local" && source != "external" {
		source = "local"
	}

	skillDir := filepath.Join(skillsRoot, name)
	if _, err := os.Stat(skillDir); err == nil && !force {
		return fmt.Errorf("skill directory %q already exists (use --force to overwrite)", skillDir)
	}
	if force {
		_ = os.RemoveAll(skillDir)
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}
	// Create scripts placeholder files
	for _, s := range scripts {
		full := filepath.Join(skillDir, filepath.FromSlash(s))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			_ = os.RemoveAll(skillDir)
			return fmt.Errorf("create script dir: %w", err)
		}
		_ = os.WriteFile(full, []byte("#!/bin/sh\n# placeholder for "+s+"\n"), 0o644)
	}
	// Build frontmatter
	var fm strings.Builder
	fm.WriteString("---\n")
	fm.WriteString(fmt.Sprintf("name: %q\n", name))
	fm.WriteString(fmt.Sprintf("description: %q\n", description))
	if category != "" {
		fm.WriteString(fmt.Sprintf("category: %q\n", category))
	}
	fm.WriteString(fmt.Sprintf("source: %q\n", source))
	if len(keywords) > 0 {
		quoted := make([]string, len(keywords))
		for i, k := range keywords {
			quoted[i] = fmt.Sprintf("%q", k)
		}
		fm.WriteString(fmt.Sprintf("activation_keywords: [%s]\n", strings.Join(quoted, ", ")))
	}
	if len(scripts) > 0 {
		quoted := make([]string, len(scripts))
		for i, s := range scripts {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		fm.WriteString(fmt.Sprintf("scripts: [%s]\n", strings.Join(quoted, ", ")))
	}
	// For external, checksum placeholder will be computed.
	body := "# " + name + "\n\n" + description + "\n\n## Instructions\n\nDescribe how this skill helps the agent.\n"
	var fullContent string
	if source == "external" {
		// Compute checksum over content without checksum line.
		withoutChecksum := fm.String() + "---\n" + body
		sum := sha256.Sum256(skill.StripChecksumLine([]byte(withoutChecksum)))
		checksum := "sha256:" + hex.EncodeToString(sum[:])
		fm.WriteString(fmt.Sprintf("checksum: %q\n", checksum))
		fullContent = fm.String() + "---\n" + body
	} else {
		fullContent = fm.String() + "---\n" + body
	}

	// Validate via skill parser
	// Use a temp dir path validation: write to skillDir and parse via parseSkillFile equivalent
	// We call skill's internal parse via writing and then validating manually using same logic.
	// Instead, we test by attempting to validate via skill's exported? We have no exported parse, but we can attempt to use the manager Scan on a temp parent.
	// Simpler: verify file can be parsed by creating a temp manager and scanning.
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(fullContent), 0o644); err != nil {
		return fmt.Errorf("write SKILL.md: %w", err)
	}
	// Quick validation: try to load via skill manager
	tmpMgr := skill.NewManager(skill.Options{ApproveExternal: true})
	defer tmpMgr.Close()
	// Scan the parent; if it fails, remove and error
	if _, err := tmpMgr.Scan(skillsRoot); err != nil {
		// Check if our skill is the one failing (Loaded should contain it or err contains name)
		// For precise check, read file and parse via internal? Use error message.
		_ = os.RemoveAll(skillDir)
		return fmt.Errorf("generated SKILL.md failed validation: %w", err)
	}
	found := false
	for _, n := range tmpMgr.Loaded() {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		_ = os.RemoveAll(skillDir)
		return fmt.Errorf("generated skill not loaded after scan")
	}

	fmt.Fprintf(out, "Created skill %q at %s\n", name, skillDir)
	fmt.Fprintf(out, "  skill: %s\n", filepath.Join(skillDir, "SKILL.md"))
	return nil
}

// RunSkillInstallForTest is an exported wrapper for runSkillInstall (used for WU7 exit verification).
func RunSkillInstallForTest(src, skillsRoot string, force, yes bool, prompter Prompter, out io.Writer) error {
	return runSkillInstall(src, skillsRoot, force, yes, prompter, out)
}

// RunSkillWizardForTest is an exported wrapper for runSkillWizard (used for WU7 wizard validity check).
func RunSkillWizardForTest(p Prompter, out io.Writer, skillsRoot string, force bool) error {
	return runSkillWizard(p, out, skillsRoot, force)
}

func stripChecksumLineBytes(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	var kept []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "checksum:") {
			continue
		}
		kept = append(kept, l)
	}
	return []byte(strings.Join(kept, "\n"))
}

// --- skill validate ---

func newSkillValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a skill SKILL.md",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			skillFile := path
			skillDir := filepath.Dir(path)
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				skillFile = filepath.Join(path, "SKILL.md")
				skillDir = path
			} else {
				skillDir = filepath.Dir(skillFile)
			}
			data, err := os.ReadFile(skillFile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid: cannot read SKILL.md %q: %v\n", skillFile, err)
				os.Exit(1)
				return nil
			}
			// Use a temp manager to validate (it also checks scripts existence and checksum)
			tmpMgr := skill.NewManager(skill.Options{ApproveExternal: true})
			defer tmpMgr.Close()
			// Scan the parent root that contains skillDir as subdirectory
			// If tmpRoot is ".forge/skills", scanning it will load this skill; we need to isolate.
			// Simpler: directly try to parse and validate via internal functions by using manager's Scan on a temp copy.
			// Create temp dir and copy skill there to avoid scanning sibling skills.
			tmpDir := os.TempDir()
			tmpScanRoot, err := os.MkdirTemp(tmpDir, "skill-validate-*")
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid: temp dir: %v\n", err)
				os.Exit(1)
				return nil
			}
			defer os.RemoveAll(tmpScanRoot)
			// Copy skillDir into tmpScanRoot/<basename>
			base := filepath.Base(skillDir)
			dest := filepath.Join(tmpScanRoot, base)
			if err := copyDir(skillDir, dest); err != nil {
				// Fallback: try to validate via direct file write
				dest = tmpScanRoot
				_ = os.MkdirAll(dest, 0o755)
				_ = os.WriteFile(filepath.Join(dest, "SKILL.md"), data, 0o644)
				// Need dir name to match name? Use base
			}
			results, err := tmpMgr.Scan(tmpScanRoot)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid: %v\n", err)
				if len(results) > 0 && results[0].Err != nil {
					fmt.Fprintf(os.Stderr, "  detail: %v\n", results[0].Err)
				}
				os.Exit(1)
				return nil
			}
			if len(results) == 0 || !results[0].Loaded {
				fmt.Fprintf(os.Stderr, "invalid: skill not loaded\n")
				os.Exit(1)
				return nil
			}
			fmt.Printf("valid: skill %q\n", base)
			return nil
		},
	}
}

// --- skill install ---

func newSkillInstallCommand() *cobra.Command {
	var force bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "install <path> [skillsRoot]",
		Short: "Install a skill directory into .forge/skills/",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			skillsRoot := filepath.Join(".", ".forge", "skills")
			if len(args) == 2 {
				skillsRoot = args[1]
			}
			var prompter Prompter
			if !yes {
				prompter = NewStdPrompter(os.Stdin, os.Stdout)
			} else {
				prompter = NewScriptedPrompter([]string{"y"})
			}
			return runSkillInstall(src, skillsRoot, force, yes, prompter, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing skill")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation for external skills")
	return cmd
}

func runSkillInstall(src, skillsRoot string, force, yes bool, prompter Prompter, out io.Writer) error {
	skillFile := src
	if fi, err := os.Stat(src); err == nil && fi.IsDir() {
		skillFile = filepath.Join(src, "SKILL.md")
	}
	data, err := os.ReadFile(skillFile)
	if err != nil {
		return fmt.Errorf("read SKILL.md %q: %w", skillFile, err)
	}
	srcDir := filepath.Dir(skillFile)
	// Validate via temp manager before install
	tmpMgr := skill.NewManager(skill.Options{ApproveExternal: true})
	defer tmpMgr.Close()
	tmpRoot, err := os.MkdirTemp("", "skill-install-validate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpRoot)
	base := filepath.Base(srcDir)
	destTmp := filepath.Join(tmpRoot, base)
	if err := copyDir(srcDir, destTmp); err != nil {
		return err
	}
	if _, err := tmpMgr.Scan(tmpRoot); err != nil {
		return fmt.Errorf("invalid skill: %w", err)
	}
	if len(tmpMgr.Loaded()) == 0 {
		return fmt.Errorf("invalid skill: not loaded")
	}
	// Determine name from loaded skill (frontmatter name must equal dir basename, already validated)
	// Use base as name
	name := base
	// Need to get skill info to check source and checksum verification
	infos := tmpMgr.Info()
	var source string
	for _, info := range infos {
		if info.Name == name {
			source = info.Source
			break
		}
	}
	// For external, need checksum already validated by tmpMgr, but also need to ensure file checksum correct
	// If source external, require confirmation
	if source == "external" {
		// Verify checksum line already validated via Scan with ApproveExternal true
		if !yes {
			if prompter == nil {
				prompter = NewScriptedPrompter(nil)
			}
			ok := prompter.Bool(fmt.Sprintf("Install external skill %q? This requires human approval", name), false)
			if !ok {
				return fmt.Errorf("install aborted: external skill requires approval")
			}
		}
	}
	destDir := filepath.Join(skillsRoot, name)
	absRoot, _ := filepath.Abs(skillsRoot)
	absDest, _ := filepath.Abs(destDir)
	if !strings.HasPrefix(absDest, absRoot+string(os.PathSeparator)) && absDest != absRoot {
		return fmt.Errorf("refusing to install outside skills root: %q", destDir)
	}
	if _, err := os.Stat(destDir); err == nil && !force {
		return fmt.Errorf("skill %q already installed at %q (use --force)", name, destDir)
	}
	if force {
		_ = os.RemoveAll(destDir)
	}
	if err := copyDir(srcDir, destDir); err != nil {
		return fmt.Errorf("copy skill: %w", err)
	}
	// For external, write approved.flag only after confirmation.
	// The approval record binds the artifact hash (these exact bytes were approved), not the directory.
	if source == "external" {
		cleaned := skill.StripChecksumLine(data)
		sum := sha256.Sum256(cleaned)
		flag := "sha256:" + hex.EncodeToString(sum[:]) + "\n"
		flagPath := filepath.Join(destDir, "approved.flag")
		_ = os.WriteFile(flagPath, []byte(flag), 0o644)
		fmt.Fprintf(out, "Installed external skill %q (approved %s)\n", name, strings.TrimSpace(flag))
	} else {
		fmt.Fprintf(out, "Installed skill %q\n", name)
	}
	return nil
}

// --- skill list ---

func newSkillListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List skills via daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.Connect(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			defer cl.Close()
			res, err := cl.SkillList(cmd.Context())
			if err != nil {
				return err
			}
			if len(res.Skills) == 0 {
				fmt.Fprintln(os.Stdout, "No skills")
				return nil
			}
			fmt.Fprintf(os.Stdout, "%-20s %-30s %-10s %-7s %s\n", "NAME", "DESCRIPTION", "CATEGORY", "ENABLED", "SOURCE")
			for _, s := range res.Skills {
				desc := s.Description
				if len(desc) > 30 {
					desc = desc[:27] + "..."
				}
				fmt.Fprintf(os.Stdout, "%-20s %-30s %-10s %-7t %s\n", s.Name, desc, s.Category, s.Enabled, s.Source)
			}
			return nil
		},
	}
}

func newSkillEnableCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a skill via daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cl, err := client.Connect(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			defer cl.Close()
			if err := cl.SkillEnable(cmd.Context(), name); err != nil {
				if isApprovalError(err) {
					return fmt.Errorf("%w (run 'forge skill install' to approve or start serve with --approve-external-plugins)", err)
				}
				var rpcErr *client.RPCError
				if errors.As(err, &rpcErr) && rpcErr.Code == daemon.ErrCodeApprovalRequired {
					return fmt.Errorf("%w (run 'forge skill install' to approve or start serve with --approve-external-plugins)", err)
				}
				return err
			}
			fmt.Fprintf(os.Stdout, "Enabled skill %q\n", name)
			return nil
		},
	}
}

func newSkillDisableCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a skill via daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cl, err := client.Connect(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			defer cl.Close()
			if err := cl.SkillDisable(cmd.Context(), name); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Disabled skill %q\n", name)
			return nil
		},
	}
}

func newSkillRemoveCommand() *cobra.Command {
	var yes bool
	var skillsRoot string
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			root := skillsRoot
			if root == "" {
				root = filepath.Join(".", ".forge", "skills")
			}
			var prompter Prompter
			if !yes {
				prompter = NewStdPrompter(os.Stdin, os.Stdout)
			} else {
				prompter = NewScriptedPrompter([]string{"y"})
			}
			return runSkillRemove(name, root, yes, prompter, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation")
	cmd.Flags().StringVar(&skillsRoot, "skills-root", "", "skills root dir")
	return cmd
}

func runSkillRemove(name, skillsRoot string, yes bool, prompter Prompter, out io.Writer) error {
	if !skillNameRe.MatchString(name) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	dir := filepath.Join(skillsRoot, name)
	absRoot, _ := filepath.Abs(skillsRoot)
	absDir, _ := filepath.Abs(dir)
	if !strings.HasPrefix(absDir, absRoot+string(os.PathSeparator)) && absDir != absRoot {
		return fmt.Errorf("refusing to remove path escaping root: %q", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("skill %q not installed", name)
	}
	if !yes {
		ok := prompter.Bool(fmt.Sprintf("Remove skill %q?", name), false)
		if !ok {
			return fmt.Errorf("remove aborted")
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove failed: %w", err)
	}
	fmt.Fprintf(out, "Removed skill %q\n", name)
	if cl, err := client.Connect(context.Background(), ""); err == nil {
		defer cl.Close()
		if _, err := cl.SkillReload(context.Background()); err != nil {
			fmt.Fprintf(out, "warning: skill reload failed: %v\n", err)
		} else {
			fmt.Fprintln(out, "Daemon skills reloaded")
		}
	}
	return nil
}

// --- skill mine ---

func newSkillMineCommand() *cobra.Command {
	var yes bool
	var force bool
	cmd := &cobra.Command{
		Use:   "mine",
		Short: "Mine skill proposals from successful sessions (RF-4.3, requires human approval per RF-4.4)",
		Long: "Analyzes sessions marked with 'forge session success' (the human input gate) and clusters similar successful trajectories. " +
			"Each cluster with >=2 members becomes a proposal written to .forge/skill-proposals/<name>/SKILL.md. " +
			"Proposals are NEVER auto-installed or auto-enabled; review the file, then run 'forge skill install <path>' (human confirmation -> approved.flag) and 'forge skill enable <name>'.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var prompter Prompter
			if yes {
				prompter = NewScriptedPrompter(nil)
			} else {
				prompter = NewStdPrompter(os.Stdin, os.Stdout)
			}
			return runSkillMine(cmd.Context(), yes, force, prompter, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "accept all defaults without prompting")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing proposal directory")
	return cmd
}

func runSkillMine(ctx context.Context, yes, force bool, prompter Prompter, out io.Writer) error {
	cl, err := client.Connect(ctx, "")
	if err != nil {
		return daemonHint(err)
	}
	defer cl.Close()

	list, err := cl.ListSessions(ctx, 1000, 0)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	var trajs []mining.Trajectory
	for _, s := range list.Sessions {
		if s.Metadata == nil {
			continue
		}
		v, ok := s.Metadata["success"]
		if !ok {
			continue
		}
		isSuccess := false
		switch vv := v.(type) {
		case bool:
			isSuccess = vv
		case string:
			isSuccess = vv == "true"
		}
		if !isSuccess {
			continue
		}
		msgsRes, err := cl.GetMessages(ctx, s.ID, 1000, 0)
		if err != nil {
			continue
		}
		traj := buildMiningTrajectory(s.ID, msgsRes.Messages)
		if len(traj.Turns) == 0 {
			continue
		}
		trajs = append(trajs, traj)
	}

	if len(trajs) == 0 {
		fmt.Fprintln(out, "No successful sessions found. Mark sessions with 'forge session success <id>' first.")
		return nil
	}

	opts := mining.Options{MinClusterSize: 2, Threshold: 0.4}
	proposals := mining.Mine(trajs, opts)

	if len(proposals) == 0 {
		fmt.Fprintln(out, "No recurring workflows found (need at least 2 similar successful sessions).")
		return nil
	}

	proposalsRoot := filepath.Join(".", ".forge", "skill-proposals")
	for _, p := range proposals {
		writeProposalIfAccepted(p, proposalsRoot, yes, force, prompter, out)
	}

	return nil
}

// writeProposalIfAccepted prints one mined proposal, asks the human for
// confirmation (unless yes), and on acceptance writes its SKILL.md under
// proposalsRoot. It reports whether a proposal file was written.
// Proposals are NEVER auto-installed or auto-enabled (RF-4.4): activation
// requires 'forge skill install <path>' (human confirmation -> approved.flag)
// followed by 'forge skill enable <name>'.
func writeProposalIfAccepted(p mining.Proposal, proposalsRoot string, yes, force bool, prompter Prompter, out io.Writer) bool {
	fmt.Fprintf(out, "\nProposal: %s\n", p.Name)
	fmt.Fprintf(out, "  Description: %s\n", p.Description)
	fmt.Fprintf(out, "  Steps: %s\n", strings.Join(p.Steps, " -> "))
	fmt.Fprintf(out, "  Source sessions: %s\n", strings.Join(p.SourceSessions, ", "))
	fmt.Fprintf(out, "  Keywords: %s\n", strings.Join(p.ActivationKeywords, ", "))

	ask := true
	if !yes {
		ask = prompter.Bool("Propose skill? [y/N]", false)
	}
	if !ask {
		fmt.Fprintf(out, "  Skipped.\n")
		return false
	}

	name := p.Name
	desc := p.Description
	category := p.Category
	keywordsStr := strings.Join(p.ActivationKeywords, ", ")
	if !yes {
		name = prompter.Line("Skill name", name)
		if !skillNameRe.MatchString(name) {
			fmt.Fprintf(out, "  Invalid name %q: must match ^[a-z][a-z0-9_-]{1,63}$ — skipping.\n", name)
			return false
		}
		desc = prompter.Line("Description", desc)
		category = prompter.Line("Category", category)
		if category == "" {
			category = "custom"
		}
		keywordsStr = prompter.Line("Activation keywords (comma-separated)", keywordsStr)
	} else {
		if !skillNameRe.MatchString(name) {
			fmt.Fprintf(out, "  Invalid derived name %q — skipping.\n", name)
			return false
		}
	}

	var keywords []string
	if strings.TrimSpace(keywordsStr) != "" {
		for _, part := range strings.Split(keywordsStr, ",") {
			kw := strings.TrimSpace(part)
			if kw != "" {
				keywords = append(keywords, kw)
			}
		}
	}

	dir := filepath.Join(proposalsRoot, name)
	if _, err := os.Stat(dir); err == nil && !force {
		fmt.Fprintf(out, "  Proposal dir %q already exists (use --force to overwrite) — skipping.\n", dir)
		return false
	}
	if force {
		_ = os.RemoveAll(dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(out, "  Failed to create dir: %v\n", err)
		return false
	}

	var fm strings.Builder
	fm.WriteString("---\n")
	fm.WriteString(fmt.Sprintf("name: %q\n", name))
	fm.WriteString(fmt.Sprintf("description: %q\n", desc))
	if category != "" {
		fm.WriteString(fmt.Sprintf("category: %q\n", category))
	}
	fm.WriteString("source: \"local\"\n")
	if len(keywords) > 0 {
		quoted := make([]string, len(keywords))
		for i, k := range keywords {
			quoted[i] = fmt.Sprintf("%q", k)
		}
		fm.WriteString(fmt.Sprintf("activation_keywords: [%s]\n", strings.Join(quoted, ", ")))
	}
	fm.WriteString("---\n")
	body := p.Instructions + "\n"
	fullContent := fm.String() + body

	skillFile := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillFile, []byte(fullContent), 0o644); err != nil {
		fmt.Fprintf(out, "  Failed to write SKILL.md: %v\n", err)
		return false
	}

	tmpMgr := skill.NewManager(skill.Options{ApproveExternal: true})
	if _, err := tmpMgr.Scan(proposalsRoot); err != nil {
		loaded := tmpMgr.Loaded()
		found := false
		for _, n := range loaded {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			_ = os.RemoveAll(dir)
			tmpMgr.Close()
			fmt.Fprintf(out, "  Generated SKILL.md failed validation: %v — removed.\n", err)
			return false
		}
	} else {
		found := false
		for _, n := range tmpMgr.Loaded() {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			_ = os.RemoveAll(dir)
			tmpMgr.Close()
			fmt.Fprintf(out, "  Generated skill not loaded after scan — removed.\n")
			return false
		}
	}
	tmpMgr.Close()

	fmt.Fprintf(out, "  Wrote proposal to %s\n", skillFile)
	fmt.Fprintf(out, "  Next steps: review the file, then 'forge skill install %s' (human confirmation -> approved.flag) and 'forge skill enable %s'\n", dir, name)
	return true
}

func buildMiningTrajectory(sessionID string, msgs []daemon.MessageResult) mining.Trajectory {
	ordered := make([]daemon.MessageResult, len(msgs))
	copy(ordered, msgs)
	if len(ordered) > 1 && ordered[0].Seq > ordered[len(ordered)-1].Seq {
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
	}

	var traj mining.Trajectory
	traj.SessionID = sessionID
	var current *mining.Turn
	var pendingToolCalls []string
	var pendingSteps []*mining.Step

	for _, m := range ordered {
		switch m.Role {
		case "user":
			if current != nil {
				traj.Turns = append(traj.Turns, *current)
			}
			current = &mining.Turn{UserPrompt: m.Content}
			pendingSteps = nil
			pendingToolCalls = nil
		case "assistant":
			if current == nil {
				current = &mining.Turn{}
			}
			for _, tc := range m.ToolCalls {
				step := mining.Step{
					ToolName:    tc.Function.Name,
					ArgsSummary: logging.Redact(tc.Function.Arguments),
				}
				current.Steps = append(current.Steps, step)
				pendingToolCalls = append(pendingToolCalls, tc.ID)
				pendingSteps = append(pendingSteps, &current.Steps[len(current.Steps)-1])
			}
		case "tool":
			found := false
			for i, id := range pendingToolCalls {
				if id == m.ToolCallID && i < len(pendingSteps) {
					pendingSteps[i].ResultSummary = logging.Redact(m.Content)
					found = true
					break
				}
			}
			if !found && current != nil && len(current.Steps) > 0 {
				last := &current.Steps[len(current.Steps)-1]
				if last.ResultSummary == "" {
					last.ResultSummary = logging.Redact(m.Content)
				}
			}
		}
	}
	if current != nil {
		traj.Turns = append(traj.Turns, *current)
	}
	var filtered []mining.Turn
	for _, t := range traj.Turns {
		if t.UserPrompt == "" && len(t.Steps) == 0 {
			continue
		}
		filtered = append(filtered, t)
	}
	traj.Turns = filtered
	return traj
}
