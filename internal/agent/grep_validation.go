package agent

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/simp-lee/oxpio/internal/model"
)

// ValidateGrepOptions checks every input that is independent of a Vault
// snapshot. It is used by the CLI before analysis so malformed requests never
// cause a Vault scan.
func ValidateGrepOptions(options GrepOptions) error {
	if options.Query == "" || strings.ContainsAny(options.Query, "\r\n") || !utf8.ValidString(options.Query) {
		return grepError("INVALID_QUERY", "parameter", "query must be a non-empty single-line UTF-8 string", 2)
	}
	if options.Scope != "" && !validGrepScope(options.Scope) {
		return grepError("INVALID_SCOPE", "parameter", "--scope must be source, body, title, path, metadata, or all", 2)
	}
	if options.Context < 0 || options.Context > 20 || options.Before < 0 || options.Before > 20 || options.After < 0 || options.After > 20 {
		return grepError("INVALID_CONTEXT", "parameter", "context, before, and after must be between 0 and 20", 2)
	}
	if (options.Context != 0 || options.ContextSet) && (options.Before != 0 || options.After != 0 || options.BeforeSet || options.AfterSet) {
		return grepError("CONTEXT_CONFLICT", "parameter", "--context is mutually exclusive with --before and --after", 2)
	}
	maxResults, maxLineLength, maxScanBytes := options.MaxResults, options.MaxLineLength, options.MaxScanBytes
	if maxResults == 0 {
		maxResults = DefaultMaxResults
	}
	if maxLineLength == 0 {
		maxLineLength = DefaultMaxLineLength
	}
	if maxScanBytes == 0 {
		maxScanBytes = DefaultMaxScanBytes
	}
	if maxResults < 1 || maxResults > 10000 || maxLineLength < 1 || maxLineLength > 8<<20 || maxScanBytes < 1 || maxScanBytes > 10<<30 {
		return grepError("INVALID_LIMIT", "parameter", "grep limits are outside the supported range", 2)
	}
	if _, err := compileGrepMatcher(options); err != nil {
		return err
	}
	if _, err := normalizeGrepDirectories(options.Path, options.Paths); err != nil {
		return grepError("INVALID_PATH", "parameter", "--path is not a normalized Vault-relative directory", 2)
	}
	if _, err := normalizeGrepDirectories(options.Section, options.Sections); err != nil {
		return grepError("INVALID_PATH", "parameter", "--section is not a normalized Vault-relative directory", 2)
	}
	for _, value := range options.Files {
		if value == "" || path.Base(value) != value || strings.ContainsAny(value, "\\/\x00\r\n") {
			return grepError("INVALID_FILE", "parameter", "--file must be a single basename", 2)
		}
	}
	if options.File != "" && (path.Base(options.File) != options.File || strings.ContainsAny(options.File, "\\/\x00\r\n")) {
		return grepError("INVALID_FILE", "parameter", "--file must be a single basename", 2)
	}
	for _, tag := range options.Tags {
		if tag == "" || model.NormalizeTagName(tag) == "" {
			return grepError("INVALID_TAG", "parameter", "--tag normalizes to an empty tag", 2)
		}
	}
	if options.Tag != "" && model.NormalizeTagName(options.Tag) == "" {
		return grepError("INVALID_TAG", "parameter", "--tag normalizes to an empty tag", 2)
	}
	for _, value := range options.Types {
		if value == "" || (value != "doc" && value != "post" && value != "page") {
			return grepError("INVALID_TYPE", "parameter", "--type must be doc, post, or page", 2)
		}
	}
	for _, value := range options.Statuses {
		if value == "" || (value != "stable" && value != "experimental" && value != "deprecated") {
			return grepError("INVALID_STATUS", "parameter", "--status must be stable, experimental, or deprecated", 2)
		}
	}
	if options.Type != "" && options.Type != "doc" && options.Type != "post" && options.Type != "page" {
		return grepError("INVALID_TYPE", "parameter", "--type must be doc, post, or page", 2)
	}
	if options.Status != "" && options.Status != "stable" && options.Status != "experimental" && options.Status != "deprecated" {
		return grepError("INVALID_STATUS", "parameter", "--status must be stable, experimental, or deprecated", 2)
	}
	return nil
}
