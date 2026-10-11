package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/spf13/cobra"
)

func newReadCommand() *cobra.Command {
	var vaultPath, outputPath string
	var includeUnpublished bool
	var offset, limit int
	cmd := &cobra.Command{
		Use:   "read PATH",
		Short: "Read an original Markdown source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("limit") && (limit < 1 || limit > 10000) {
				return fmt.Errorf("limit must be between 1 and 10000 when provided")
			}
			if offset < 0 {
				return fmt.Errorf("offset must be >= 0")
			}
			snapshot, err := internalapplication.OpenSnapshot(vaultPath, outputPath)
			if err != nil {
				return err
			}
			result, err := internalagent.Read(snapshot, args[0], includeUnpublished, offset, limit)
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
			_, err = io.WriteString(cmd.OutOrStdout(), result.Content)
			return err
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Include unpublished source")
	cmd.Flags().IntVar(&offset, "offset", 0, "0-based starting line")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum number of lines; zero means all")
	return cmd
}

func newFindCommand() *cobra.Command {
	var vaultPath, outputPath, pathPrefix, kind, section, publish string
	var includeUnpublished bool
	cmd := &cobra.Command{
		Use:   "find",
		Short: "Find normalized Markdown sources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := internalapplication.OpenSnapshot(vaultPath, outputPath)
			if err != nil {
				return err
			}
			var publishFilter *bool
			if publish != "" {
				value, parseErr := strconv.ParseBool(publish)
				if parseErr != nil {
					return fmt.Errorf("--publish must be true or false")
				}
				publishFilter = &value
				if !includeUnpublished && !value {
					return internalapplication.ErrHiddenSource
				}
			}
			result, err := internalagent.Find(snapshot, includeUnpublished, pathPrefix, kind, section, publishFilter)
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
			for _, item := range result.Items {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", item.Path, item.Kind, item.Title); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&pathPrefix, "path", "", "Vault-relative path prefix")
	cmd.Flags().StringVar(&kind, "kind", "", "section, article, or all")
	cmd.Flags().StringVar(&section, "section", "", "Vault-relative section path")
	cmd.Flags().StringVar(&publish, "publish", "", "true or false")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Include unpublished source")
	return cmd
}

func newLSCommand() *cobra.Command {
	var vaultPath, outputPath string
	var includeUnpublished bool
	cmd := &cobra.Command{
		Use:   "ls PATH",
		Short: "List Vault files and directories",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := internalapplication.OpenSnapshot(vaultPath, outputPath)
			if err != nil {
				return err
			}
			items, err := snapshot.ListDirectory(args[0], includeUnpublished)
			if err != nil {
				return err
			}
			data, err := json.Marshal(struct {
				Items []internalapplication.DirectoryEntry `json:"items"`
			}{Items: items})
			if err != nil {
				return err
			}
			if formatOf(cmd) == "json" {
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			for _, item := range items {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), item.Path); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Include unpublished source")
	return cmd
}

func newTagsCommand() *cobra.Command {
	var vaultPath, outputPath string
	var includeUnpublished bool
	cmd := &cobra.Command{
		Use:   "tags",
		Short: "List normalized published tags",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := internalapplication.OpenSnapshot(vaultPath, outputPath)
			if err != nil {
				return err
			}
			result := internalagent.Tags(snapshot, includeUnpublished)
			data, err := json.Marshal(struct {
				Items []internalagent.TagResult `json:"items"`
			}{Items: result})
			if err != nil {
				return err
			}
			if formatOf(cmd) == "json" {
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			for _, item := range result {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", item.Name, strings.Join(item.Paths, ",")); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	cmd.Flags().BoolVar(&includeUnpublished, "include-unpublished", false, "Include unpublished source")
	return cmd
}
