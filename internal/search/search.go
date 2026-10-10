// Package search contains the bounded, native source-matching primitives used
// by the Agent grep adapter. It has no filesystem, process, or index dependency.
package search

import (
	"errors"
	"regexp"
	"regexp/syntax"
)

var ErrZeroWidth = errors.New("pattern can match an empty string")

func Compile(query string, useRegex, caseSensitive bool) (*regexp.Regexp, error) {
	pattern := query
	if !useRegex {
		pattern = regexp.QuoteMeta(pattern)
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	matcher, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	if nullable(parsed) {
		return nil, ErrZeroWidth
	}
	return matcher, nil
}

func nullable(expr *syntax.Regexp) bool {
	switch expr.Op {
	case syntax.OpNoMatch, syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar:
		return false
	case syntax.OpLiteral:
		return len(expr.Rune) == 0
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpCapture:
		return nullable(expr.Sub[0])
	case syntax.OpConcat:
		for _, child := range expr.Sub {
			if !nullable(child) {
				return false
			}
		}
		return true
	case syntax.OpAlternate:
		for _, child := range expr.Sub {
			if nullable(child) {
				return true
			}
		}
		return false
	case syntax.OpQuest, syntax.OpStar:
		return true
	case syntax.OpPlus:
		return nullable(expr.Sub[0])
	case syntax.OpRepeat:
		return expr.Min == 0 || nullable(expr.Sub[0])
	default:
		return false
	}
}
