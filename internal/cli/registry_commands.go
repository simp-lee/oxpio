package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/spf13/cobra"
)

func newHelpCommand() *cobra.Command {
	var agentWorkflow bool
	var includeUnpublished bool
	var vaultPath string
	var requestedCommand string
	cmd := &cobra.Command{
		Use:   "help [COMMAND]",
		Short: "Show OXPIO command help",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 2 {
				return fmt.Errorf("help accepts at most two command words")
			}
			if len(args) > 0 {
				requestedCommand = strings.Join(strings.Fields(strings.Join(args, " ")), " ")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if requestedCommand != "" {
				if _, ok := internalapplication.FindCommand(requestedCommand); !ok && len(internalapplication.CommandGroup(requestedCommand)) == 0 {
					return fmt.Errorf("unknown command %q", requestedCommand)
				}
			}
			if includeUnpublished && strings.TrimSpace(vaultPath) == "" {
				return fmt.Errorf("--include-unpublished requires --vault")
			}
			var dynamic internalapplication.Context
			var dynamicSet bool
			if strings.TrimSpace(vaultPath) != "" {
				loaded, err := internalapplication.LoadContext(vaultPath, "", includeUnpublished)
				if err != nil {
					return err
				}
				dynamic, dynamicSet = loaded, true
			}
			if formatOf(cmd) == "json" {
				data, err := internalapplication.HelpJSON(agentWorkflow)
				if requestedCommand != "" {
					if spec, ok := internalapplication.FindCommand(requestedCommand); ok {
						data, err = json.Marshal(struct {
							Schema  string                          `json:"schema"`
							Command internalapplication.CommandSpec `json:"command"`
						}{Schema: "oxpio.agent/help/v1", Command: spec})
					} else if group := internalapplication.CommandGroup(requestedCommand); len(group) != 0 {
						data, err = json.Marshal(struct {
							Schema   string                            `json:"schema"`
							Commands []internalapplication.CommandSpec `json:"commands"`
						}{Schema: "oxpio.agent/help/v1", Commands: group})
					} else {
						return fmt.Errorf("unknown command %q", requestedCommand)
					}
				}
				if dynamicSet {
					var dynamicData any
					if err := json.Unmarshal(data, &dynamicData); err != nil {
						return err
					}
					data, err = json.Marshal(struct {
						Help    json.RawMessage              `json:"help"`
						Context *internalapplication.Context `json:"context,omitempty"`
					}{Help: data, Context: &dynamic})
				}
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			text := internalapplication.HelpText(requestedCommand, agentWorkflow)
			if dynamicSet {
				text += fmt.Sprintf("Selected theme: %s\nSources: %d\n", dynamic.Theme, dynamic.Sources)
			}
			_, err := io.WriteString(cmd.OutOrStdout(), text)
			return err
		},
	}
	cmd.Flags().BoolVar(&agentWorkflow, "agent", false, "Show the concise Agent workflow")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Include unpublished capability details")
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	return cmd
}

func newContextCommand() *cobra.Command {
	var vaultPath string
	var outputPath string
	var includeUnpublished bool
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Show Agent capabilities and Vault context",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if includeUnpublished && strings.TrimSpace(vaultPath) == "" {
				return fmt.Errorf("--include-unpublished requires --vault")
			}
			context := internalapplication.StaticContext()
			if strings.TrimSpace(vaultPath) != "" {
				loaded, err := internalapplication.LoadContext(vaultPath, outputPath, includeUnpublished)
				if err != nil {
					return err
				}
				context = loaded
			}
			data, err := json.Marshal(context)
			if err != nil {
				return err
			}
			if formatOf(cmd) == "json" {
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "API: %s\nTheme: %s\nCapabilities: %s\n", context.APIVersion, context.Theme, strings.Join(context.Capabilities, ", "))
			return err
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Include unpublished source")
	return cmd
}

func newRegistryPlaceholder(spec internalapplication.CommandSpec) *cobra.Command {
	nameParts := strings.Split(spec.Name, " ")
	use := nameParts[len(nameParts)-1]
	if spec.Arguments != "" {
		use += " " + spec.Arguments
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: spec.Summary,
		Args:  registryArgumentValidator(spec),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("%s is not available yet", spec.Name)
		},
	}
	for _, flag := range spec.Flags {
		// --format is a root persistent flag and must not be shadowed by a
		// placeholder's local flag; every other flag comes from the registry.
		if flag.Name == "" || flag.Name == "format" || flag.Name == "version" {
			continue
		}
		if flag.Value != "" {
			cmd.Flags().String(flag.Name, "", flag.Summary)
		} else {
			cmd.Flags().Bool(flag.Name, false, flag.Summary)
		}
		if flag.Required {
			if err := cmd.MarkFlagRequired(flag.Name); err != nil {
				panic(err)
			}
		}
	}
	return cmd
}

func registryArgumentValidator(spec internalapplication.CommandSpec) func(*cobra.Command, []string) error {
	arguments := strings.Fields(spec.Arguments)
	if len(arguments) == 0 {
		return cobra.NoArgs
	}
	if len(arguments) == 1 && strings.HasPrefix(arguments[0], "[") && strings.HasSuffix(arguments[0], "]") {
		return cobra.MaximumNArgs(1)
	}
	return cobra.ExactArgs(len(arguments))
}
