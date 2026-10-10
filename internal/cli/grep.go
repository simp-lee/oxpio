package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	internalagent "github.com/simp-lee/oxpio/internal/agent"
	internalapplication "github.com/simp-lee/oxpio/internal/application"
	"github.com/spf13/cobra"
)

func newGrepCommand() *cobra.Command {
	var vaultPath, outputPath, scope string
	var pathPrefixes, fileNames, tags, sectionPrefixes, types, statuses, publishes []string
	var regex, caseSensitive, includeUnpublished bool
	var context, before, after, maxResults, maxLineLength int
	var maxScanBytes int64
	cmd := &cobra.Command{
		Use: "grep QUERY", Short: "Search Markdown source with literal or RE2 matching", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			if flags.Changed("context") && (flags.Changed("before") || flags.Changed("after")) {
				return internalagent.GrepError{Code: "CONTEXT_CONFLICT", Category: "parameter", Message: "--context is mutually exclusive with --before and --after", Exit: 2}
			}
			for _, name := range []string{"max-results", "max-line-length", "max-scan-bytes"} {
				if flags.Changed(name) {
					if name == "max-scan-bytes" {
						scan, _ := flags.GetInt64(name)
						if scan == 0 {
							return internalagent.GrepError{Code: "INVALID_LIMIT", Category: "parameter", Message: name + " must be at least 1", Exit: 2}
						}
					} else {
						value, _ := flags.GetInt(name)
						if value == 0 {
							return internalagent.GrepError{Code: "INVALID_LIMIT", Category: "parameter", Message: name + " must be at least 1", Exit: 2}
						}
					}
				}
			}
			publishValues := append([]string{}, publishes...)
			publishFilters := make([]bool, 0, len(publishValues))
			for _, raw := range publishValues {
				value, parseErr := parseBoolFlag(raw)
				if parseErr != nil {
					return parseErr
				}
				if !includeUnpublished && !value {
					return internalagent.GrepError{Code: "PERMISSION_DENIED", Category: "permission", Message: "source is not available in the selected read scope", Exit: 4}
				}
				publishFilters = append(publishFilters, value)
			}
			options := internalagent.GrepOptions{
				Query: args[0], Regex: regex, CaseSensitive: caseSensitive, Scope: scope,
				Context: context, Before: before, After: after, ContextSet: flags.Changed("context"), BeforeSet: flags.Changed("before"), AfterSet: flags.Changed("after"), MaxResults: maxResults,
				MaxLineLength: maxLineLength, MaxScanBytes: maxScanBytes, Paths: pathPrefixes, PathSet: flags.Changed("path"),
				Files: fileNames, Tags: tags, Sections: sectionPrefixes, SectionSet: flags.Changed("section"), Types: types, Statuses: statuses,
				Publishes: publishFilters, IncludeUnpublished: includeUnpublished,
			}
			if err := internalagent.ValidateGrepOptions(options); err != nil {
				return err
			}
			snapshot, err := internalapplication.OpenSnapshot(vaultPath, outputPath)
			if err != nil {
				if errors.Is(err, internalapplication.ErrInvalidSource) {
					return internalagent.GrepError{Code: "INVALID_SOURCE", Category: "source", Message: err.Error(), Exit: 1}
				}
				return err
			}
			result, err := internalagent.Grep(snapshot, options)
			if err != nil {
				return err
			}
			if formatOf(cmd) == "json" {
				data, marshalErr := json.Marshal(result)
				if marshalErr != nil {
					return marshalErr
				}
				_, err = cmd.OutOrStdout().Write(append(data, '\n'))
				return err
			}
			hasContext := len(result.Contexts) != 0
			for _, match := range result.Matches {
				if hasContext && match.Line != nil && (match.SourceKind == "source" || match.SourceKind == "body") {
					continue
				}
				if match.Line == nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", match.Path, match.SourceKind, match.MatchedText)
					continue
				}
				lineText := match.LineText
				if lineText == "" {
					lineText = match.MatchedText
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s:%d:%d: %s\n", match.Path, *match.Line, valueOrZero(match.Column), lineText)
			}
			for _, window := range result.Contexts {
				for offset, line := range window.Lines {
					fmt.Fprintf(cmd.OutOrStdout(), "%s:%d: %s\n", window.Path, window.StartLine+offset, line)
				}
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&vaultPath, "vault", "", "Path to the OXPIO Vault")
	flags.StringVar(&outputPath, "output", "", "Formal output boundary")
	flags.BoolVar(&regex, "regex", false, "Use RE2 matching")
	flags.BoolVar(&caseSensitive, "case-sensitive", false, "Disable Unicode folding")
	flags.StringVar(&scope, "scope", "source", "source, body, title, path, metadata, or all")
	flags.IntVar(&context, "context", 0, "Context lines before and after (0..20)")
	flags.IntVar(&before, "before", 0, "Context lines before (0..20)")
	flags.IntVar(&after, "after", 0, "Context lines after (0..20)")
	flags.IntVar(&maxResults, "max-results", internalagent.DefaultMaxResults, "Maximum matches (1..10000)")
	flags.IntVar(&maxLineLength, "max-line-length", internalagent.DefaultMaxLineLength, "Maximum logical line bytes (1..8388608)")
	flags.Int64Var(&maxScanBytes, "max-scan-bytes", internalagent.DefaultMaxScanBytes, "Maximum source bytes to scan (1..10737418240)")
	flags.StringArrayVar(&pathPrefixes, "path", nil, "Vault-relative path prefix (repeatable)")
	flags.StringArrayVar(&fileNames, "file", nil, "Exact basename (repeatable)")
	flags.StringArrayVar(&tags, "tag", nil, "Normalized tag (repeatable)")
	flags.StringArrayVar(&sectionPrefixes, "section", nil, "Vault-relative section prefix (repeatable)")
	flags.StringArrayVar(&types, "type", nil, "doc, post, or page (repeatable)")
	flags.StringArrayVar(&statuses, "status", nil, "stable, experimental, or deprecated (repeatable)")
	flags.StringArrayVar(&publishes, "publish", nil, "true or false (repeatable)")
	flags.BoolVar(&includeUnpublished, "include-unpublished", false, "Include unpublished source")
	return cmd
}

func parseBoolFlag(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, internalagent.GrepError{Code: "INVALID_PUBLISH", Category: "parameter", Message: "--publish must be true or false", Exit: 2}
}

func valueOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
