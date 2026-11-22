package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/deepblue-phoenix/gligabao/internal/sargo"
	sargoembed "github.com/deepblue-phoenix/gligabao/rust/sargo"
)

// sargoCmd is `gligabao sargo`: runs the sargo server that is embedded in this binary (see rust/sargo/embed.go), or prints its default config.
func sargoCmd() *cobra.Command {
	var configPath string
	// These commands do not use gligabao.toml.
	noConfig := func(cmd *cobra.Command,
		args []string) error {
		return nil
	}
	root := &cobra.Command{
		Use:               "sargo",
		Short:             "Run the sargo model router embedded in this binary",
		PersistentPreRunE: noConfig,
	}
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the embedded sargo server in the foreground",
		Long: `Extracts the sargo binary embedded in this gligabao binary and runs it as a server until interrupted.
It uses the default sargo config embedded too (see "gligabao sargo config"), unless --sargo-config (or $SARGO_CONFIG) names
a config file, which is used instead. The API keys are read from the environment (ANTHROPIC_API_KEY, OPENAI_API_KEY, or the
variables the config names).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command,
			args []string) error {
			cmd.SilenceUsage = true
			ctx, stop := signal.NotifyContext(cmd.Context(),
				os.Interrupt,
				syscall.SIGTERM)
			defer stop()
			server, err := sargo.StartServer(ctx,
				sargo.ServerOptions{
					ConfigPath: configPath,
					Stdout:     cmd.OutOrStdout(),
					Stderr:     cmd.ErrOrStderr(),
				})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "sargo listening on %s (config %s)\n",
				server.Addr,
				server.ConfigPath)
			exited := make(chan error, 1)
			go func() { exited <- server.Wait() }()
			select {
			case <-ctx.Done():
				return server.Stop()
			case waitErr := <-exited:
				_ = server.Stop()
				return fmt.Errorf("sargo exited: %w", waitErr)
			}
		},
	}
	serve.Flags().StringVar(&configPath,
		"sargo-config",
		os.Getenv("SARGO_CONFIG"),
		"sargo config file to use instead of the embedded default (env SARGO_CONFIG)")
	config := &cobra.Command{
		Use:   "config",
		Short: "Print the default sargo config embedded in this binary",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command,
			args []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), sargoembed.Config)
			return err
		},
	}
	root.AddCommand(serve, config)
	return root
}
