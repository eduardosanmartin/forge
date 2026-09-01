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
