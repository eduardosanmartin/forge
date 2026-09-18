package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newDaemonCommand())
}

func newDaemonCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage daemon settings",
	}
	cmd.AddCommand(newSetPasswordCommand())
	cmd.AddCommand(newSetProviderCommand())
	return cmd
}

func newSetPasswordCommand() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "set-password",
		Short: "Set (or clear) the daemon's remote-access auth token (RF-7.4)",
		Long: "Hashes (SHA-256) a shared token/password and writes it to ~/.forge/config.json\n" +
			"as daemon.auth_token_hash — the raw token itself is never stored. This is\n" +
			"required, together with TLS (--tls-cert/--tls-key or --tls-self-signed), before\n" +
			"`forge serve --addr` can bind beyond loopback (RNF-4.11's safety floor); a\n" +
			"loopback-only daemon does not need it.\n\n" +
			"Reads the password from stdin. Prefer piping it in so it never touches shell\n" +
			"history or a process listing:\n" +
			"  printf '%s' 'my password' | forge daemon set-password\n" +
			"Typing it interactively works too, but it will echo to the terminal.\n\n" +
			"The CLI (forge run/chat/...) sends this same token back as a Bearer header via\n" +
			"the FORGE_DAEMON_TOKEN environment variable when talking to a remote daemon;\n" +
			"a local/loopback daemon needs neither the token configured nor set.",
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := config.GlobalConfigPath()
			if err != nil {
				return fmt.Errorf("resolve global config path: %w", err)
			}

			if clear {
				if err := setDaemonAuthTokenHash(path, ""); err != nil {
					return fmt.Errorf("update config: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cleared the daemon auth token in %s (loopback binds no longer require it; non-loopback binds now refuse to start).\n", path)
				return nil
			}

			token, err := readPasswordLine(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if strings.TrimSpace(token) == "" {
				return &UsageError{Err: fmt.Errorf("password must not be empty")}
			}

			if err := setDaemonAuthTokenHash(path, daemon.HashToken(token)); err != nil {
				return fmt.Errorf("update config: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Daemon auth token set in %s (the raw token was not stored).\n", path)
			fmt.Fprintln(cmd.OutOrStdout(), "Export FORGE_DAEMON_TOKEN with this same value on any machine that runs the forge CLI against this daemon remotely.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "remove the configured token instead of setting one")
	return cmd
}

// readPasswordLine reads one line from r, trimming the trailing newline. A
// final line with no trailing newline (common when piped via `printf`, which
// emits none) is accepted too — io.EOF after some bytes is not an error here.
func readPasswordLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// setDaemonAuthTokenHash updates ONLY the daemon.auth_token_hash key of the
// JSON document at path, leaving every other key (including sections this
// process never loaded, like a provider's api_key that lives only in a
// different layered config file) byte-for-byte alone. It deliberately does
// NOT go through config.Config/Save: that marshals the full in-memory
// Config, which by this point is the *merged* result of every layered
// config file (global ~/.forge/config.json + project ./.forge/config.json,
// see config.Load) — saving that merged document back to a single file
// would duplicate secrets from one layer into the other. Round-tripping
// through a generic map avoids that entirely.
func setDaemonAuthTokenHash(path, hash string) error {
	raw := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse existing config %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", path, err)
	}

	section, _ := raw["daemon"].(map[string]any)
	if section == nil {
		section = map[string]any{}
	}
	if hash == "" {
		delete(section, "auth_token_hash")
	} else {
		section["auth_token_hash"] = hash
	}
	if len(section) == 0 {
		delete(raw, "daemon")
	} else {
		raw["daemon"] = section
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	return nil
}
