package cli

import (
	"bufio"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tanvoxyz/tanvo/apps/server/internal/app"
	"github.com/tanvoxyz/tanvo/apps/server/internal/config"
	"github.com/tanvoxyz/tanvo/libs/auth"
)

// Environment variables of `tanvo admin bootstrap`'s secrets, which stay
// off the command line.
const (
	// BootstrapPasswordEnv is the admin user's password, unless
	// --password-stdin is given.
	BootstrapPasswordEnv = "TANVO_BOOTSTRAP_PASSWORD"
	// BootstrapAPIKeyEnv is an API key the organization must have.
	BootstrapAPIKeyEnv = "TANVO_BOOTSTRAP_API_KEY"
)

func newAdmin(env *Env, file *string) *cobra.Command {
	admin := &cobra.Command{
		Use:   "admin",
		Short: "Administer the installation",
		Args:  cobra.NoArgs,
	}
	admin.AddCommand(newBootstrap(env, file), newServiceTokenKey(env, file))
	return admin
}

func newServiceTokenKey(env *Env, file *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service-token-key",
		Short: "Manage the keys signing the service tokens of the enterprise services",
		Args:  cobra.NoArgs,
	}
	var keyID string
	generate := &cobra.Command{
		Use:   "generate",
		Short: "Print a new service token signing key",
		Long: "Print a new Ed25519 key as <key id>:<seed>, the seed in base64url without padding. Put it first in\n" +
			"$TANVO_SERVICE_TOKEN_KEYS (comma-separated) to sign with it; keep the previous key after it until the\n" +
			"tokens it signed have expired (15 minutes). The output is a secret.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := auth.GenerateServiceTokenKey(rand.Reader, keyID)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), k.Encode())
			return err
		},
	}
	generate.Flags().StringVar(&keyID, "key-id", "", "ID of the key: letters, digits, - or _ (default: 8 random hexadecimal digits)")
	public := &cobra.Command{
		Use:   "public",
		Short: "Print the public keys of the configured service token keys",
		Long: "Print, comma-separated, <key id>:<public key> for each key of $TANVO_SERVICE_TOKEN_KEYS, the public key in\n" +
			"base64url without padding: the value of the enterprise services' $TANVO_ANALYTICS_TOKEN_KEYS.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, warnings, err := config.Load(config.Source{Environ: env.Environ, File: *file})
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "config: "+w)
			}
			keys, err := cfg.ServiceTokenKeyList()
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				return errors.New("no service token key: set $TANVO_SERVICE_TOKEN_KEYS (tanvo admin service-token-key generate)")
			}
			pubs := make([]string, len(keys))
			for i, k := range keys {
				pubs[i] = k.Public().String()
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(pubs, ","))
			return err
		},
	}
	cmd.AddCommand(generate, public)
	return cmd
}

func newBootstrap(env *Env, file *string) *cobra.Command {
	var (
		in            app.BootstrapInput
		passwordStdin bool
	)
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Ensure an organization, its admin user and an API key exist",
		Long: "Find or create the organization named --organization-name with its default billing entity, the user\n" +
			"--email with an admin membership in it, and an API key. An existing user keeps its password. The\n" +
			"password of a created user is read from stdin with --password-stdin, else from $" + BootstrapPasswordEnv + ".\n" +
			"$" + BootstrapAPIKeyEnv + ", when set, is an API key the organization must have; otherwise a key is\n" +
			"generated when the organization has none, and printed once. Running it again changes nothing.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			ctx := cmd.Context()
			in.Password, in.APIKey = lookupEnv(env, BootstrapPasswordEnv), lookupEnv(env, BootstrapAPIKeyEnv)
			if passwordStdin {
				line, rerr := bufio.NewReader(env.Stdin).ReadString('\n')
				if rerr != nil && (!errors.Is(rerr, io.EOF) || line == "") {
					return fmt.Errorf("--password-stdin: read the password: %w", rerr)
				}
				in.Password = strings.TrimRight(line, "\r\n")
			}
			if in.Password == "" {
				return fmt.Errorf("bootstrap: a password is required (--password-stdin or $%s)", BootstrapPasswordEnv)
			}
			cfg, tel, err := setup(ctx, env, *file, "admin")
			if err != nil {
				return err
			}
			defer shutdownTelemetry(ctx, tel, &err)
			res, err := app.Bootstrap(ctx, cfg, in, tel.TracerProvider, tel.MeterProvider)
			if err != nil {
				tel.Logger.ErrorContext(ctx, "tanvo: bootstrap", "error", err)
				return err
			}
			tel.Logger.InfoContext(ctx, "tanvo: bootstrap done", "organization_id", res.Organization.ID.String(), "created", res.Created)
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "organization_id: %s\nuser_id: %s\n", res.Organization.ID, res.User.ID); err != nil {
				return err
			}
			if res.APIKey != "" {
				_, err = fmt.Fprintf(out, "api_key: %s\n", res.APIKey)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&in.OrganizationName, "organization-name", "", "name of the organization (required)")
	cmd.Flags().StringVar(&in.Email, "email", "", "email of the admin user (required)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the admin user's password from the first line of stdin")
	_ = cmd.MarkFlagRequired("organization-name")
	_ = cmd.MarkFlagRequired("email")
	return cmd
}

// lookupEnv returns the variable key of env.Environ (os.Environ when nil).
func lookupEnv(env *Env, key string) string {
	if env.Environ == nil {
		return os.Getenv(key)
	}
	var v string
	for _, kv := range env.Environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}
