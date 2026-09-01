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
	"github.com/eduardosanmartin/forge/internal/plugin"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newPluginCommand())
}

func newPluginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage forge plugins",
	}
	cmd.AddCommand(newPluginNewCommand())
	cmd.AddCommand(newPluginValidateCommand())
	cmd.AddCommand(newPluginInstallCommand())
	cmd.AddCommand(newPluginListCommand())
	cmd.AddCommand(newPluginEnableCommand())
	cmd.AddCommand(newPluginDisableCommand())
	cmd.AddCommand(newPluginRemoveCommand())
	return cmd
}

// --- plugin new wizard ---

func newPluginNewCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Create a new plugin scaffold (interactive)",
		RunE: func(cmd *cobra.Command, args []string) error {
			prompter := NewStdPrompter(os.Stdin, os.Stdout)
			root := filepath.Join(".", "forge-plugins")
			return runPluginWizard(prompter, os.Stdout, root, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing directory")
	return cmd
}

var pluginNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

func runPluginWizard(p Prompter, out io.Writer, pluginsRoot string, force bool) error {
	var name string
	for {
		raw := p.Line("Plugin name (e.g. my_plugin)", "")
		if raw == "" {
			fmt.Fprintln(out, "name is required")
			continue
		}
		if !pluginNameRe.MatchString(raw) {
			fmt.Fprintf(out, "invalid name %q: must match ^[a-z][a-z0-9_]{1,63}$\n", raw)
			continue
		}
		name = raw
		break
	}
	version := p.Line("Version", "0.1.0")
	if version == "" {
		version = "0.1.0"
	}
	if !versionRe.MatchString(version) {
		fmt.Fprintf(out, "invalid version %q, using 0.1.0\n", version)
		version = "0.1.0"
	}
	description := p.Line("Description", fmt.Sprintf("%s plugin", name))
	if strings.TrimSpace(description) == "" {
		description = fmt.Sprintf("%s plugin", name)
	}
	permKinds := []string{"fs.read", "fs.write", "shell.exec", "git", "net"}
	var selected []string
	for _, k := range permKinds {
		if p.Bool(fmt.Sprintf("Enable permission %q?", k), false) {
			selected = append(selected, k)
		}
	}
	if len(selected) == 0 {
		selected = []string{"fs.read"}
	}
	entrypoint := p.Line("Entrypoint filename", "plugin.wasm")
	if entrypoint == "" {
		entrypoint = "plugin.wasm"
	}
	source := p.Choose("Source", []string{"local", "external"}, "local")
	if source != "local" && source != "external" {
		source = "local"
	}

	pluginDir := filepath.Join(pluginsRoot, name)
	if _, err := os.Stat(pluginDir); err == nil && !force {
		return fmt.Errorf("plugin directory %q already exists (use --force to overwrite)", pluginDir)
	}
	if force {
		_ = os.RemoveAll(pluginDir)
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return fmt.Errorf("create plugin dir: %w", err)
	}
	permissionsStr := ""
	if len(selected) > 0 {
		quoted := make([]string, len(selected))
		for i, v := range selected {
			quoted[i] = fmt.Sprintf("%q", v)
		}
		permissionsStr = strings.Join(quoted, ", ")
	}
	toolPerm := selected[0]
	toolName := name + "_hello"
	dependenciesStr := "[]"
	checksumLine := ""
	if source == "external" {
		checksumLine = "checksum = \"sha256:" + strings.Repeat("0", 64) + "\"\n"
	}
	manifest := fmt.Sprintf("name = %q\nversion = %q\ndescription = %q\nsource = %q\nentrypoint = %q\npermissions = [%s]\ndependencies = %s\n%s\n[[tools]]\nname = %q\ndescription = \"Hello tool\"\npermission = %q\n", name, version, description, source, entrypoint, permissionsStr, dependenciesStr, checksumLine, toolName, toolPerm)

	if _, err := plugin.ParseManifest([]byte(manifest)); err != nil {
		_ = os.RemoveAll(pluginDir)
		return fmt.Errorf("generated manifest failed validation: %w", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.toml"), []byte(manifest), 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	cargo := fmt.Sprintf("[package]\nname = %q\nversion = %q\nedition = \"2021\"\n\n[lib]\ncrate-type = [\"cdylib\"]\n\n[dependencies]\n", name, version)
	if err := os.WriteFile(filepath.Join(pluginDir, "Cargo.toml"), []byte(cargo), 0o644); err != nil {
		return fmt.Errorf("write Cargo.toml: %w", err)
	}
	srcDir := filepath.Join(pluginDir, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	libRs := generatePluginLibRs(name, toolName, toolPerm)
	if err := os.WriteFile(filepath.Join(srcDir, "lib.rs"), []byte(libRs), 0o644); err != nil {
		return fmt.Errorf("write lib.rs: %w", err)
	}
	readme := fmt.Sprintf("# %s\n\n%s\n\nThis plugin was scaffolded by `forge plugin new`. Scaffold verified in WU6.\n", name, description)
	_ = os.WriteFile(filepath.Join(pluginDir, "README.md"), []byte(readme), 0o644)
	_ = os.WriteFile(filepath.Join(pluginDir, ".gitignore"), []byte("/target\n*.wasm\n"), 0o644)

	fmt.Fprintf(out, "Created plugin %q at %s\n", name, pluginDir)
	fmt.Fprintf(out, "  manifest: %s\n", filepath.Join(pluginDir, "manifest.toml"))
	fmt.Fprintf(out, "  rust: %s\n", filepath.Join(srcDir, "lib.rs"))
	return nil
}

// GeneratePluginLibRs is the exported wrapper for generatePluginLibRs (used for WU6 dogfood regen test).
func GeneratePluginLibRs(pluginName, toolName, perm string) string {
	return generatePluginLibRs(pluginName, toolName, perm)
}

// RunPluginWizardForTest is an exported wrapper for runPluginWizard (used for WU6 regen test).
func RunPluginWizardForTest(p Prompter, out io.Writer, pluginsRoot string, force bool) error {
	return runPluginWizard(p, out, pluginsRoot, force)
}

func generatePluginLibRs(pluginName, toolName, perm string) string {
	return fmt.Sprintf("#![allow(static_mut_refs, dead_code)]\n// Scaffold for forge plugin %q — verified in WU6.\n// ABIVersion = 1 (FROZEN). This crate is a scaffold; build with `cargo build --target wasm32-unknown-unknown`.\n\nstatic mut HEAP: [u8; 4194304] = [0; 4194304];\nstatic mut HEAP_POS: usize = 0;\n\n#[link(wasm_import_module = \"forge_host\")]\nextern \"C\" {\n    fn log(level_ptr: i32, level_len: i32, msg_ptr: i32, msg_len: i32);\n    fn fs_read(path_ptr: i32, path_len: i32) -> i64;\n    fn fs_write(path_ptr: i32, path_len: i32, data_ptr: i32, data_len: i32) -> i32;\n    fn shell_exec(cmd_ptr: i32, cmd_len: i32, args_ptr: i32, args_len: i32) -> i64;\n    fn git_run(args_ptr: i32, args_len: i32) -> i64;\n    fn net_fetch(url_ptr: i32, url_len: i32) -> i64;\n}\n\n#[no_mangle]\npub extern \"C\" fn forge_abi_version() -> i32 { 1 }\n\n#[no_mangle]\npub extern \"C\" fn forge_alloc(size: i32) -> i32 {\n    unsafe {\n        let pos = HEAP_POS;\n        HEAP_POS += size as usize;\n        if HEAP_POS > HEAP.len() { return 0; }\n        HEAP.as_mut_ptr().add(pos) as i32\n    }\n}\n\nfn pack(ptr: i32, len: i32) -> i64 {\n    ((ptr as i64) << 32) | (len as i64 & 0xffffffff)\n}\n\n#[no_mangle]\npub extern \"C\" fn forge_tool_list() -> i64 {\n    let json = r#\"[{\"name\":\"%s\",\"description\":\"Hello tool\",\"permission\":\"%s\"}]\"#;\n    let ptr = forge_alloc(json.len() as i32);\n    if ptr == 0 { return 0; }\n    unsafe { std::ptr::copy_nonoverlapping(json.as_ptr(), HEAP.as_mut_ptr().add(HEAP_POS - json.len()) as *mut u8, json.len()); }\n    pack(ptr, json.len() as i32)\n}\n\n#[no_mangle]\npub extern \"C\" fn forge_tool_invoke(_fn_ptr: i32, _fn_len: i32, _args_ptr: i32, _args_len: i32) -> i64 {\n    let json = r#\"{\"result\":\"hello from %s\"}\"#;\n    let ptr = forge_alloc(json.len() as i32);\n    if ptr == 0 { return 0; }\n    unsafe { std::ptr::copy_nonoverlapping(json.as_ptr(), HEAP.as_mut_ptr().add(HEAP_POS - json.len()) as *mut u8, json.len()); }\n    pack(ptr, json.len() as i32)\n}\n", pluginName, toolName, perm, pluginName)
}

// --- plugin validate ---

func newPluginValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <path>",
		Short: "Validate a plugin manifest and entrypoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			manifestPath := path
			if fi, err := os.Stat(path); err == nil && fi.IsDir() {
				manifestPath = filepath.Join(path, "manifest.toml")
			}
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid: cannot read manifest %q: %v\n", manifestPath, err)
				os.Exit(1)
				return nil
			}
			m, err := plugin.ParseManifest(data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid: %v\n", err)
				os.Exit(1)
				return nil
			}
			pluginDir := filepath.Dir(manifestPath)
			entryPath := filepath.Join(pluginDir, filepath.FromSlash(m.Entrypoint))
			if _, err := os.Stat(entryPath); err != nil {
				fmt.Fprintf(os.Stderr, "invalid: entrypoint %q not found: %v\n", m.Entrypoint, err)
				os.Exit(1)
				return nil
			}
			if m.Source == plugin.SourceExternal {
				wasmBytes, err := os.ReadFile(entryPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid: cannot read entrypoint: %v\n", err)
					os.Exit(1)
					return nil
				}
				sum := sha256.Sum256(wasmBytes)
				got := "sha256:" + hex.EncodeToString(sum[:])
				if !strings.EqualFold(got, m.Checksum) {
					fmt.Fprintf(os.Stderr, "invalid: checksum mismatch: want %s got %s\n", m.Checksum, got)
					os.Exit(1)
					return nil
				}
			}
			fmt.Printf("valid: plugin %q version %q (%d tools)\n", m.Name, m.Version, len(m.Tools))
			return nil
		},
	}
}

// --- plugin install ---

func newPluginInstallCommand() *cobra.Command {
	var force bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "install <path> [pluginsRoot]",
		Short: "Install a plugin directory into forge-plugins/",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			pluginsRoot := filepath.Join(".", "forge-plugins")
			if len(args) == 2 {
				pluginsRoot = args[1]
			}
			var prompter Prompter
			if !yes {
				prompter = NewStdPrompter(os.Stdin, os.Stdout)
			} else {
				prompter = NewScriptedPrompter([]string{"y"})
			}
			return runPluginInstall(src, pluginsRoot, force, yes, prompter, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing plugin")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation for external plugins")
	return cmd
}

func runPluginInstall(src, pluginsRoot string, force, yes bool, prompter Prompter, out io.Writer) error {
	manifestPath := src
	if fi, err := os.Stat(src); err == nil && fi.IsDir() {
		manifestPath = filepath.Join(src, "manifest.toml")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest %q: %w", manifestPath, err)
	}
	m, err := plugin.ParseManifest(data)
	if err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}
	srcDir := filepath.Dir(manifestPath)
	entryPath := filepath.Join(srcDir, filepath.FromSlash(m.Entrypoint))
	wasmBytes, err := os.ReadFile(entryPath)
	if err != nil {
		return fmt.Errorf("entrypoint %q not found: %w", m.Entrypoint, err)
	}
	if m.Source == plugin.SourceExternal {
		sum := sha256.Sum256(wasmBytes)
		got := "sha256:" + hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, m.Checksum) {
			return fmt.Errorf("checksum mismatch for external plugin: want %s got %s", m.Checksum, got)
		}
		if !yes {
			if prompter == nil {
				prompter = NewScriptedPrompter(nil)
			}
			ok := prompter.Bool(fmt.Sprintf("Install external plugin %q? This requires human approval", m.Name), false)
			if !ok {
				return fmt.Errorf("install aborted: external plugin requires approval")
			}
		}
	}
	destDir := filepath.Join(pluginsRoot, m.Name)
	absRoot, _ := filepath.Abs(pluginsRoot)
	absDest, _ := filepath.Abs(destDir)
	if !strings.HasPrefix(absDest, absRoot+string(os.PathSeparator)) && absDest != absRoot {
		return fmt.Errorf("refusing to install outside plugins root: %q", destDir)
	}
	if _, err := os.Stat(destDir); err == nil && !force {
		return fmt.Errorf("plugin %q already installed at %q (use --force)", m.Name, destDir)
	}
	if force {
		_ = os.RemoveAll(destDir)
	}
	if err := copyDir(srcDir, destDir); err != nil {
		return fmt.Errorf("copy plugin: %w", err)
	}
	if m.Source == plugin.SourceExternal {
		// The approval record binds the artifact hash (these exact bytes were approved), not the directory.
		sum := sha256.Sum256(wasmBytes)
		flag := "sha256:" + hex.EncodeToString(sum[:]) + "\n"
		flagPath := filepath.Join(destDir, "approved.flag")
		_ = os.WriteFile(flagPath, []byte(flag), 0o644)
		fmt.Fprintf(out, "Installed external plugin %q (approved %s)\n", m.Name, strings.TrimSpace(flag))
	} else {
		fmt.Fprintf(out, "Installed plugin %q\n", m.Name)
	}
	return nil
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(s)
			if err != nil {
				return err
			}
			if err := os.WriteFile(d, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- plugin list ---

func newPluginListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List plugins via daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, err := client.Connect(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			defer cl.Close()
			res, err := cl.PluginList(cmd.Context())
			if err != nil {
				return err
			}
			if len(res.Plugins) == 0 {
				fmt.Fprintln(os.Stdout, "No plugins")
				return nil
			}
			fmt.Fprintf(os.Stdout, "%-20s %-10s %-8s %-7s %s\n", "NAME", "VERSION", "SOURCE", "ENABLED", "TOOLS")
			for _, p := range res.Plugins {
				fmt.Fprintf(os.Stdout, "%-20s %-10s %-8s %-7t %d\n", p.Name, p.Version, p.Source, p.Enabled, p.ToolCount)
			}
			return nil
		},
	}
}

// --- plugin enable/disable ---

func newPluginEnableCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <name>",
		Short: "Enable a plugin via daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cl, err := client.Connect(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			defer cl.Close()
			if err := cl.PluginEnable(cmd.Context(), name); err != nil {
				if isApprovalError(err) {
					return fmt.Errorf("%w (run 'forge plugin install' to approve or start serve with --approve-external-plugins)", err)
				}
				var rpcErr *client.RPCError
				if errors.As(err, &rpcErr) && rpcErr.Code == daemon.ErrCodeApprovalRequired {
					return fmt.Errorf("%w (run 'forge plugin install' to approve or start serve with --approve-external-plugins)", err)
				}
				return err
			}
			fmt.Fprintf(os.Stdout, "Enabled plugin %q\n", name)
			return nil
		},
	}
}

func newPluginDisableCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <name>",
		Short: "Disable a plugin via daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			cl, err := client.Connect(cmd.Context(), "")
			if err != nil {
				return fmt.Errorf("daemon not reachable: %w", err)
			}
			defer cl.Close()
			if err := cl.PluginDisable(cmd.Context(), name); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "Disabled plugin %q\n", name)
			return nil
		},
	}
}

func newPluginRemoveCommand() *cobra.Command {
	var yes bool
	var pluginsRoot string
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			root := pluginsRoot
			if root == "" {
				root = filepath.Join(".", "forge-plugins")
			}
			var prompter Prompter
			if !yes {
				prompter = NewStdPrompter(os.Stdin, os.Stdout)
			} else {
				prompter = NewScriptedPrompter([]string{"y"})
			}
			return runPluginRemove(name, root, yes, prompter, os.Stdout)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip confirmation")
	cmd.Flags().StringVar(&pluginsRoot, "plugins-root", "", "plugins root dir")
	return cmd
}

func runPluginRemove(name, pluginsRoot string, yes bool, prompter Prompter, out io.Writer) error {
	if !pluginNameRe.MatchString(name) {
		return fmt.Errorf("invalid plugin name %q", name)
	}
	dir := filepath.Join(pluginsRoot, name)
	absRoot, _ := filepath.Abs(pluginsRoot)
	absDir, _ := filepath.Abs(dir)
	if !strings.HasPrefix(absDir, absRoot+string(os.PathSeparator)) && absDir != absRoot {
		return fmt.Errorf("refusing to remove path escaping root: %q", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("plugin %q not installed", name)
	}
	if !yes {
		ok := prompter.Bool(fmt.Sprintf("Remove plugin %q?", name), false)
		if !ok {
			return fmt.Errorf("remove aborted")
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove failed: %w", err)
	}
	fmt.Fprintf(out, "Removed plugin %q\n", name)
	if cl, err := client.Connect(context.Background(), ""); err == nil {
		defer cl.Close()
		if _, err := cl.PluginReload(context.Background()); err != nil {
			fmt.Fprintf(out, "warning: plugin reload failed: %v\n", err)
		} else {
			fmt.Fprintln(out, "Daemon plugins reloaded")
		}
	}
	return nil
}

func isApprovalError(err error) bool {
	return strings.Contains(err.Error(), "requires explicit approval")
}

