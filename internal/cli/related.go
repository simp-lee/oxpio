package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/spf13/cobra"
)

func newRelatedCommand() *cobra.Command {
	var vaultPath, outputPath string
	cmd := &cobra.Command{
		Use:   "related PATH",
		Short: "Return production related-article recommendations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := internalapplication.OpenSnapshot(vaultPath, outputPath)
			if err != nil {
				return err
			}
			result, err := internalagent.Related(snapshot, args[0])
			if err != nil {
				return err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return err
			}
			if formatOf(cmd) == "json" {
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			if _, err = fmt.Fprintf(cmd.OutOrStdout(), "source: %s\npartition: %s\n", result.Source.Path, result.Partition); err != nil {
				return err
			}
			for _, candidate := range result.Candidates {
				if _, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%g\t%s\t%s\n", candidate.Path, candidate.Score, strings.Join(candidate.Reasons, ","), candidate.Title); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	return cmd
}
