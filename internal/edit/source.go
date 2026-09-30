package edit

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// NewArticleSource renders the editor-supported new-article templates.
// It intentionally uses no clock or filesystem metadata.
func NewArticleSource(title, typeName, date string) ([]byte, error) {
	title = strings.TrimSpace(title)
	typeName = strings.TrimSpace(typeName)
	if title == "" {
		return nil, fmt.Errorf("title must be non-empty")
	}
	if typeName == "" {
		typeName = "doc"
	}
	if typeName != "doc" && typeName != "post" && typeName != "page" {
		return nil, fmt.Errorf("type must be doc, post, or page")
	}
	date = strings.TrimSpace(date)
	if typeName == "post" {
		if date == "" {
			return nil, fmt.Errorf("date is required for type=post")
		}
		if _, err := time.Parse("2006-01-02", date); err != nil {
			if _, rfc3339Err := time.Parse(time.RFC3339, date); rfc3339Err != nil {
				return nil, fmt.Errorf("date must be YYYY-MM-DD or RFC3339")
			}
		}
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "---\ntitle: %s\npublish: false\ntype: %s\n", strconv.Quote(title), typeName)
	if typeName == "post" {
		_, _ = fmt.Fprintf(&builder, "date: %s\n", strconv.Quote(date))
	}
	builder.WriteString("---\n\n")
	return []byte(builder.String()), nil
}

// NewSectionSource renders an unpublished section index template.
func NewSectionSource(title string) ([]byte, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("title must be non-empty")
	}
	return []byte(fmt.Sprintf("---\ntitle: %s\npublish: false\n---\n\n", strconv.Quote(title))), nil
}
