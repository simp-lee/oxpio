package diag

import (
	"fmt"
	"sort"
	"sync"
)

// Severity classifies a diagnostic as either a warning or an error.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Kind identifies the category of a diagnostic.
type Kind string

const (
	KindDeadLink          Kind = "deadlink"
	KindSlugConflict      Kind = "slug_conflict"
	KindStructuredData    Kind = "structured_data"
	KindUnsupportedSyntax Kind = "unsupported_syntax"
	KindUnresolvedAsset   Kind = "unresolved_asset"
	KindOutputCleanup     Kind = "output_cleanup"
	KindReferenceConflict Kind = "reference_conflict"
	KindSchema            Kind = "schema"
	KindSection           Kind = "section"
	KindNavigation        Kind = "navigation"
	KindRoute             Kind = "route"
	KindVersion           Kind = "version"
	KindMetadata          Kind = "metadata"
	KindOrder             Kind = "order"
)

// Location pinpoints the source of a diagnostic in a vault file.
type Location struct {
	Path string
	Line int
}

// Diagnostic is a structured build warning or error.
type Diagnostic struct {
	Severity Severity
	Kind     Kind
	Location Location
	// Field and Target make schema/link diagnostics machine-actionable while
	// remaining optional for older parser diagnostics.
	Field   string
	Target  string
	Message string
}

// Collector accumulates diagnostics and supports concurrent writers and merges.
//
// The zero value is ready for use.
type Collector struct {
	mu          sync.RWMutex
	diagnostics []Diagnostic
	warnings    int
	errors      int
}

// NewCollector constructs an empty diagnostic collector.
func NewCollector() *Collector {
	return &Collector{}
}

// Add stores a diagnostic.
func (c *Collector) Add(d Diagnostic) {
	c.appendDiagnostics([]Diagnostic{d})
}

// Warningf records a warning diagnostic.
func (c *Collector) Warningf(kind Kind, location Location, format string, args ...any) {
	c.addf(SeverityWarning, kind, location, format, args...)
}

// Errorf records an error diagnostic.
func (c *Collector) Errorf(kind Kind, location Location, format string, args ...any) {
	c.addf(SeverityError, kind, location, format, args...)
}

// Merge copies diagnostics from the provided collectors into the receiver.
func (c *Collector) Merge(others ...*Collector) {
	if c == nil {
		return
	}

	for _, other := range others {
		if other == nil || other == c {
			continue
		}
		c.appendDiagnostics(other.snapshot())
	}
}

// Diagnostics returns a stable, sorted copy of all diagnostics.
func (c *Collector) Diagnostics() []Diagnostic {
	diagnostics := c.snapshot()
	sortDiagnostics(diagnostics)
	return diagnostics
}

// Warnings returns the warning diagnostics.
func (c *Collector) Warnings() []Diagnostic {
	return c.bySeverity(SeverityWarning)
}

// Errors returns the error diagnostics.
func (c *Collector) Errors() []Diagnostic {
	return c.bySeverity(SeverityError)
}

// Len reports the number of collected diagnostics.
func (c *Collector) Len() int {
	if c == nil {
		return 0
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return len(c.diagnostics)
}

// WarningCount reports the number of warnings.
func (c *Collector) WarningCount() int {
	if c == nil {
		return 0
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.warnings
}

// ErrorCount reports the number of errors.
func (c *Collector) ErrorCount() int {
	if c == nil {
		return 0
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.errors
}

// HasWarnings reports whether any warnings were collected.
func (c *Collector) HasWarnings() bool {
	return c.WarningCount() > 0
}

// HasErrors reports whether any errors were collected.
func (c *Collector) HasErrors() bool {
	return c.ErrorCount() > 0
}

func (c *Collector) addf(severity Severity, kind Kind, location Location, format string, args ...any) {
	message := format
	if len(args) > 0 {
		message = fmt.Sprintf(format, args...)
	}

	c.Add(Diagnostic{
		Severity: severity,
		Kind:     kind,
		Location: location,
		Message:  message,
	})
}

func (c *Collector) bySeverity(severity Severity) []Diagnostic {
	diagnostics := c.Diagnostics()
	filtered := make([]Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == severity {
			filtered = append(filtered, diagnostic)
		}
	}
	return filtered
}

func (c *Collector) snapshot() []Diagnostic {
	if c == nil {
		return nil
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	diagnostics := make([]Diagnostic, len(c.diagnostics))
	copy(diagnostics, c.diagnostics)
	return diagnostics
}

func (c *Collector) appendDiagnostics(diagnostics []Diagnostic) {
	if c == nil || len(diagnostics) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.diagnostics = append(c.diagnostics, diagnostics...)
	for _, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case SeverityWarning:
			c.warnings++
		case SeverityError:
			c.errors++
		}
	}
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.Slice(diagnostics, func(i, j int) bool {
		left := diagnostics[i]
		right := diagnostics[j]

		if left.Location.Path != right.Location.Path {
			return left.Location.Path < right.Location.Path
		}
		if left.Location.Line != right.Location.Line {
			return left.Location.Line < right.Location.Line
		}
		if severityOrder(left.Severity) != severityOrder(right.Severity) {
			return severityOrder(left.Severity) < severityOrder(right.Severity)
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Field != right.Field {
			return left.Field < right.Field
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		return left.Message < right.Message
	})
}

func severityOrder(severity Severity) int {
	switch severity {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}
