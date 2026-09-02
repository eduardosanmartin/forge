package cli

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/eduardosanmartin/forge/internal/approval"
	"github.com/spf13/cobra"
)

func init() {
	RootCommand.AddCommand(newKeygenCommand())
}

func newKeygenCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate ed25519 keypair for signing approvals",
		Long:  "Generates an ed25519 keypair in the user config directory (os.UserConfigDir()/forge/keys/forge.key, forge.pub). The private key is 0600, public is 0644. Refuses to overwrite without --force. Prints the public key fingerprint.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pub, _, err := approval.Keygen(force)
			if err != nil {
				return err
			}
			fp := approval.Fingerprint(pub)
			fmt.Fprintf(os.Stdout, "Generated keypair in %s\n", mustKeysDir())
			fmt.Fprintf(os.Stdout, "Public key fingerprint (sha256): %s\n", fp)
			fmt.Fprintf(os.Stdout, "Public key (base64): %s\n", mustPubB64(pub))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing keypair")
	return cmd
}

func mustKeysDir() string {
	dir, err := approval.KeysDir()
	if err != nil {
		return "<unknown>"
	}
	return dir
}

func mustPubB64(pub []byte) string {
	return base64.StdEncoding.EncodeToString(pub)
}
