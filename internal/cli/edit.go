package cli

import (
	"fmt"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internaledit "github.com/simp-lee/oxpio/internal/edit"
	internalserver "github.com/simp-lee/oxpio/internal/server"
	"github.com/spf13/cobra"
)

func newEditCommand(deps commandDependencies) *cobra.Command {
	var vaultPath string
	var outputPath string
	var port int
	var setup bool

	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Run the explicitly enabled single-account editor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if setup {
				if cmd.Flags().Changed("output") || cmd.Flags().Changed("port") {
					return fmt.Errorf("edit --setup accepts only --vault")
				}
				resolvedVault, err := resolveVaultPath(vaultPath)
				if err != nil {
					return err
				}
				return internaledit.Setup(resolvedVault, cmd.InOrStdin(), cmd.OutOrStdout())
			}
			if err := validateCommandPort(port, cmd.Flags().Changed("port")); err != nil {
				return err
			}
			boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
			if err != nil {
				return err
			}
			buildResult, err := deps.buildSiteWithOptions(boundary.VaultPath, boundary.OutputPath, internalbuild.Options{DiagnosticsWriter: cmd.ErrOrStderr()})
			if err != nil {
				return fmt.Errorf("build site: %w", err)
			}
			if deps.newEditServer == nil {
				return fmt.Errorf("edit server is unavailable")
			}
			srv, err := deps.newEditServer(boundary.VaultPath, boundary.OutputPath, port, buildResult.Catalog)
			if err != nil {
				return fmt.Errorf("create edit server: %w", err)
			}
			srv.EnableLiveReload()
			if err := srv.ListenAndServe(); err != nil {
				return fmt.Errorf("listen and serve: %w", err)
			}
			return nil
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&vaultPath, "vault", "", "Path to the Obsidian vault (default current directory)")
	flags.StringVar(&outputPath, "output", "", "Path to write the generated site (default <vault>/public)")
	flags.IntVar(&port, "port", 0, fmt.Sprintf("Port for the editor server (default %d)", internalserver.DefaultPort))
	flags.BoolVar(&setup, "setup", false, "Interactively configure the single editor account and exit")
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
