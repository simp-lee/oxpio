package cli

import (
	"encoding/json"
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
				resolvedVault, err := resolveVaultPath(vaultPath)
				if err != nil {
					return err
				}
				interactiveOutput := cmd.OutOrStdout()
				if formatOf(cmd) == "json" {
					interactiveOutput = cmd.ErrOrStderr()
				}
				if resetPassword {
					if err := internaledit.ResetPassword(resolvedVault, cmd.InOrStdin(), interactiveOutput); err != nil {
						return err
					}
				} else if err := internaledit.Setup(resolvedVault, cmd.InOrStdin(), interactiveOutput); err != nil {
					return err
				}
				if formatOf(cmd) == "json" {
					data, err := json.Marshal(map[string]any{"configured": true})
					if err != nil {
						return err
					}
					return writeCommandData(cmd, data, nil)
				}
				return nil
			}
			if listen != "" && listen != "127.0.0.1" && listen != "0.0.0.0" {
				return fmt.Errorf("editor listen address must be 127.0.0.1 or 0.0.0.0")
			}
			if err := validateCommandPort(port, cmd.Flags().Changed("port")); err != nil {
				return err
			}
			boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
			if err != nil {
				return err
			}
			if err := internaledit.Preflight(boundary.VaultPath, boundary.OutputPath, port, listen); err != nil {
				return err
			}
			published, err := publishWithDependencies(deps, boundary.VaultPath, boundary.OutputPath, internalapplication.PublishOptions{DiagnosticsWriter: cmd.ErrOrStderr()})
			if err != nil {
				return fmt.Errorf("publish site: %w", err)
			}
			buildResult := published.Build
			if buildResult == nil {
				if published.Published {
					return writeEditorPostCommitWarning(cmd, boundary.VaultPath, boundary.OutputPath, published, errors.New("publish site returned no build result"))
				}
				return fmt.Errorf("publish site returned no build result")
			}
			postCommitWarning := buildResult.OutputCleanupError
			announcementData := map[string]any{}
			if postCommitWarning != nil {
				announcementData["postCommitWarning"] = true
				announcementData["warning"] = sanitizeCLIError(postCommitWarning, []string{"--vault", boundary.VaultPath, "--output", boundary.OutputPath})
			}
			if deps.newEditServer == nil {
				return writeEditorPostCommitWarning(cmd, boundary.VaultPath, boundary.OutputPath, published, errors.Join(postCommitWarning, errors.New("editor server is unavailable")))
			}
			srv, err := deps.newEditServer(boundary.VaultPath, boundary.OutputPath, port, listen, buildResult.Catalog)
			if err != nil {
				return writeEditorPostCommitWarning(cmd, boundary.VaultPath, boundary.OutputPath, published, errors.Join(postCommitWarning, fmt.Errorf("create edit server: %w", err), closePreviewServer(srv)))
			}
			srv.EnableLiveReload()
			listenErr := listenAndAnnounceWithData(cmd, srv, listen, port, announcementData)
			closeErr := closePreviewServer(srv)
			if listenErr != nil {
				warning := errors.Join(postCommitWarning, fmt.Errorf("listen and serve: %w", listenErr), closeErr)
				if editorStartupAnnounced(cmd) {
					return &postCommitWarningError{warning: warning}
				}
				return writeEditorPostCommitWarning(cmd, boundary.VaultPath, boundary.OutputPath, published, warning)
			}
			if closeErr != nil {
				warning := errors.Join(postCommitWarning, fmt.Errorf("close editor server: %w", closeErr))
				if editorStartupAnnounced(cmd) {
					return &postCommitWarningError{warning: warning}
				}
				return writeEditorPostCommitWarning(cmd, boundary.VaultPath, boundary.OutputPath, published, warning)
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

func editorStartupAnnounced(cmd *cobra.Command) bool {
	writer, ok := cmd.OutOrStdout().(*streamingServiceJSONWriter)
	return ok && writer.sent
}

type postCommitWarningError struct {
	warning error
}

func (e *postCommitWarningError) Error() string {
	if e == nil || e.warning == nil {
		return "post-commit warning"
	}
	return e.warning.Error()
}

func (e *postCommitWarningError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.warning
}

func writeEditorPostCommitWarning(cmd *cobra.Command, vaultPath, outputPath string, result *internalapplication.PublishResult, warning error) error {
	if result == nil || !result.Published {
		return warning
	}
	message := sanitizeCLIError(warning, []string{"--vault", vaultPath, "--output", outputPath})
	data := map[string]any{
		"output":            internalapplication.OutputDescriptor(vaultPath, outputPath),
		"preview":           false,
		"published":         true,
		"postCommitWarning": true,
		"warning":           message,
		"changes":           result.Changes,
	}
	if formatOf(cmd) == "json" {
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(append(encoded, '\n'))
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Output changes: %d\n", len(result.Changes)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "POST_COMMIT_WARNING: %s\n", message)
	return err
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
