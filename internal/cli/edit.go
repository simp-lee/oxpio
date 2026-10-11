package cli

import (
	"errors"
	"fmt"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	internaledit "github.com/simp-lee/oxpio/internal/edit"
	internalserver "github.com/simp-lee/oxpio/internal/server"
	"github.com/spf13/cobra"
)

func newEditorCommand(deps commandDependencies) *cobra.Command {
	var vaultPath string
	var outputPath string
	var port int
	var listen string
	var setup bool
	var resetPassword bool

	cmd := &cobra.Command{
		Use:   "editor",
		Short: "Run the explicitly enabled single-account editor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if setup || resetPassword {
				if setup && resetPassword {
					return fmt.Errorf("editor --setup and --reset-password are mutually exclusive")
				}
				if cmd.Flags().Changed("output") || cmd.Flags().Changed("port") || cmd.Flags().Changed("listen") {
					return fmt.Errorf("editor setup/reset accepts only --vault")
				}
				if resetPassword {
					return fmt.Errorf("editor password reset is not available")
				}
				resolvedVault, err := resolveVaultPath(vaultPath)
				if err != nil {
					return err
				}
				return internaledit.Setup(resolvedVault, cmd.InOrStdin(), cmd.OutOrStdout())
			}
			if listen != "" && listen != "127.0.0.1" {
				return fmt.Errorf("editor listen address %q is not available", listen)
			}
			if err := validateCommandPort(port, cmd.Flags().Changed("port")); err != nil {
				return err
			}
			boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
			if err != nil {
				return err
			}
			published, err := publishWithDependencies(deps, boundary.VaultPath, boundary.OutputPath, internalapplication.PublishOptions{DiagnosticsWriter: cmd.ErrOrStderr()})
			if err != nil {
				return fmt.Errorf("publish site: %w", err)
			}
			buildResult := published.Build
			if buildResult == nil {
				return fmt.Errorf("publish site returned no build result")
			}
			if deps.newEditServer == nil {
				return fmt.Errorf("editor server is unavailable")
			}
			srv, err := deps.newEditServer(boundary.VaultPath, boundary.OutputPath, port, buildResult.Catalog)
			if err != nil {
				return errors.Join(fmt.Errorf("create edit server: %w", err), closePreviewServer(srv))
			}
			srv.EnableLiveReload()
			listenErr := listenAndAnnounce(cmd, srv, listen, port)
			closeErr := closePreviewServer(srv)
			if listenErr != nil {
				return errors.Join(fmt.Errorf("listen and serve: %w", listenErr), closeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close editor server: %w", closeErr)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&vaultPath, "vault", "", "Path to the OXPIO Vault (default current directory)")
	flags.StringVar(&outputPath, "output", "", "Path to write the generated site (default <vault>/public)")
	flags.StringVar(&listen, "listen", "127.0.0.1", "Listener bind address")
	flags.IntVar(&port, "port", 0, fmt.Sprintf("Port for the editor server (default %d)", internalserver.DefaultPort))
	flags.BoolVar(&setup, "setup", false, "Interactively configure the single editor account and exit")
	flags.BoolVar(&resetPassword, "reset-password", false, "Interactively reset the editor password")
	return cmd
}

func validateCommandPort(port int, explicit bool) error {
	if !explicit && port == 0 {
		return nil
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
