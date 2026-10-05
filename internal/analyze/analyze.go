// Package analyze provides the read-only validation handoff shared by the
// validate command and strict builds.
package analyze

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"

	diagnostic "github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/siteplan"
)

// Result is the complete read-only analysis result. No output path is opened or
// modified by this package.
type Result struct {
	Plan        *siteplan.Result
	Diagnostics []diagnostic.Diagnostic
}

// Analyze runs the strict configuration, source, section, route, collection,
// and version checks without inferring an output boundary. Callers that plan
// to publish must use AnalyzeWithOutput with their formal output path.
func Analyze(vaultPath string) (Result, error) {
	return AnalyzeWithOutput(vaultPath, "")
}

// AnalyzeWithOutput uses the same strict plan for publication while excluding
// the resolved output root from the vault source tree.
func AnalyzeWithOutput(vaultPath, outputPath string) (Result, error) {
	return AnalyzeWithOutputAndConcurrency(vaultPath, outputPath, 0)
}

// AnalyzeWithOutputAndConcurrency uses the same strict plan for publication,
// optionally selecting the worker bound used by independent indexing work.
func AnalyzeWithOutputAndConcurrency(vaultPath, outputPath string, concurrency int) (Result, error) {
	return AnalyzeWithOutputAndConcurrencyAndOverlay(vaultPath, outputPath, concurrency, nil, nil)
}

// AnalyzeWithOutputAndConcurrencyAndOverlay shares the normal analyzer while
// reading candidate Markdown bytes from an immutable edit overlay.
func AnalyzeWithOutputAndConcurrencyAndOverlay(vaultPath, outputPath string, concurrency int, overlay map[string][]byte, deleted map[string]bool) (Result, error) {
	var planned *siteplan.Result
	var err error
	if len(overlay) == 0 && len(deleted) == 0 && outputPath == "" {
		planned, err = siteplan.BuildWithConcurrency(vaultPath, concurrency)
	} else if len(overlay) == 0 && len(deleted) == 0 {
		planned, err = siteplan.BuildForOutputWithConcurrency(vaultPath, outputPath, concurrency)
	} else if outputPath == "" {
		// Overlay builds always use an explicit output boundary so the
		// candidate cannot accidentally scan generated output.
		planned, err = siteplan.BuildForOutputWithOverlay(vaultPath, filepath.Join(vaultPath, "public"), overlay, deleted, concurrency)
	} else {
		planned, err = siteplan.BuildForOutputWithOverlay(vaultPath, outputPath, overlay, deleted, concurrency)
	}
	if planned == nil {
		collector := diagnostic.NewCollector()
		if err != nil {
			collector.Add(analyzeErrorDiagnostic(vaultPath, err))
		}
		return Result{Diagnostics: collector.Diagnostics()}, err
	}
	return Result{Plan: planned, Diagnostics: append([]diagnostic.Diagnostic(nil), planned.Diagnostics...)}, err
}

var (
	analyzeErrorPathPattern   = regexp.MustCompile(`(?:config|article|section|frontmatter) "([^"]+)"`)
	analyzeErrorLinePattern   = regexp.MustCompile(`\bline ([0-9]+)\b`)
	analyzeErrorFieldPattern  = regexp.MustCompile(`(?:field|key) "([^"]+)"|\b([A-Za-z][A-Za-z0-9_.\[\]]*):|\b([A-Za-z][A-Za-z0-9_.\[\]]*) (?:is|required|must)`)
	analyzeErrorTargetPattern = regexp.MustCompile(`(?:link|target|resource|asset) "([^"]+)"`)
)

func analyzeErrorDiagnostic(vaultPath string, err error) diagnostic.Diagnostic {
	item := diagnostic.Diagnostic{Severity: diagnostic.SeverityError, Kind: diagnostic.KindSchema, Location: analyzeErrorLocation(vaultPath, err)}
	if err == nil {
		return item
	}
	message := err.Error()
	if match := analyzeErrorFieldPattern.FindStringSubmatch(message); len(match) > 1 {
		for _, value := range match[1:] {
			if value != "" {
				item.Field = value
				break
			}
		}
	}
	if match := analyzeErrorTargetPattern.FindStringSubmatch(message); len(match) == 2 {
		item.Target = match[1]
	}
	item.Message = message
	return item
}

func analyzeErrorLocation(vaultPath string, err error) diagnostic.Location {
	location := diagnostic.Location{Path: filepath.Join(vaultPath, "oxpio.yaml")}
	if err == nil {
		return location
	}
	if match := analyzeErrorPathPattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		candidate := match[1]
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(vaultPath, filepath.FromSlash(candidate))
		}
		location.Path = candidate
	}
	if match := analyzeErrorLinePattern.FindStringSubmatch(err.Error()); len(match) == 2 {
		location.Line, _ = strconv.Atoi(match[1])
	}
	return location
}

// HasFailures reports whether either severity makes the selected operation
// invalid. validate and strict build both use this policy.
func HasFailures(diagnostics []diagnostic.Diagnostic) bool { return len(diagnostics) != 0 }

// WriteDiagnostics renders the stable human-readable diagnostic format.
func WriteDiagnostics(writer io.Writer, diagnostics []diagnostic.Diagnostic) error {
	if writer == nil {
		return nil
	}
	for _, item := range diagnostics {
		line := item.Location.Line
		location := item.Location.Path
		if line > 0 {
			location = fmt.Sprintf("%s:%d", location, line)
		}
		if item.Field != "" {
			location += " [field=" + item.Field + "]"
		}
		if item.Target != "" {
			location += " [target=" + item.Target + "]"
		}
		if _, err := fmt.Fprintf(writer, "%s %s %s: %s\n", item.Severity, item.Kind, location, item.Message); err != nil {
			return err
		}
	}
	return nil
}

// Failure turns a non-empty diagnostic set into an operation error.
func Failure(diagnostics []diagnostic.Diagnostic) error {
	if len(diagnostics) == 0 {
		return nil
	}
	var errorsCount, warningsCount int
	for _, item := range diagnostics {
		switch item.Severity {
		case diagnostic.SeverityError:
			errorsCount++
		case diagnostic.SeverityWarning:
			warningsCount++
		}
	}
	return fmt.Errorf("validation failed with %d error(s) and %d warning(s)", errorsCount, warningsCount)
}
