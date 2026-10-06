package asset

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/tdewolff/parse/v2/css"
)

const (
	svgNamespace   = "http://www.w3.org/2000/svg"
	xlinkNamespace = "http://www.w3.org/1999/xlink"
	xmlNamespace   = "http://www.w3.org/XML/1998/namespace"
)

var xmlDeclarationPattern = regexp.MustCompile(`^[ \t\r\n]*version[ \t\r\n]*=[ \t\r\n]*("1\.[01]"|'1\.[01]')([ \t\r\n]+encoding[ \t\r\n]*=[ \t\r\n]*("[A-Za-z][A-Za-z0-9._-]*"|'[A-Za-z][A-Za-z0-9._-]*'))?([ \t\r\n]+standalone[ \t\r\n]*=[ \t\r\n]*("(yes|no)"|'(yes|no)'))?[ \t\r\n]*$`)

// ValidateLocalSVG requires a well-formed SVG whose image, stylesheet, and
// font references are entirely internal fragment references.
func ValidateLocalSVG(data []byte) error {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	decoder := xml.NewDecoder(bytes.NewReader(data))
	seenRoot := false
	seenXMLDeclaration := false
	seenPreambleContent := false
	rootNamespace := ""
	depth := 0
	styleDepth := 0
	namespaceStack := make([]map[string]string, 0)
	var styleContent strings.Builder
	for {
		tokenStart := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decode SVG XML: %w", err)
		}
		switch value := token.(type) {
		case xml.StartElement:
			seenPreambleContent = true
			if strings.EqualFold(value.Name.Local, "script") {
				return fmt.Errorf("SVG script elements are not allowed")
			}
			attributeNames := make(map[xml.Name]struct{}, len(value.Attr))
			for _, attribute := range value.Attr {
				if _, exists := attributeNames[attribute.Name]; exists {
					return fmt.Errorf("duplicate SVG attribute %q is not allowed", attribute.Name.Local)
				}
				attributeNames[attribute.Name] = struct{}{}
			}
			namespaces := map[string]string{"xml": xmlNamespace}
			if len(namespaceStack) != 0 {
				for prefix, namespace := range namespaceStack[len(namespaceStack)-1] {
					namespaces[prefix] = namespace
				}
			}
			for _, attribute := range value.Attr {
				switch {
				case attribute.Name.Space == "xmlns":
					namespaces[attribute.Name.Local] = attribute.Value
				case attribute.Name.Space == "" && attribute.Name.Local == "xmlns":
					namespaces[""] = attribute.Value
				}
			}
			namespaceStack = append(namespaceStack, namespaces)
			if depth == 0 {
				if seenRoot {
					return fmt.Errorf("SVG document must contain exactly one root element")
				}
				if value.Name.Local != "svg" || value.Name.Space != "" && value.Name.Space != svgNamespace {
					return fmt.Errorf("root element must be svg")
				}
				seenRoot = true
				rootNamespace = value.Name.Space
			}
			depth++
			svgElement := isSVGElement(value.Name, rootNamespace)
			if svgElement && strings.EqualFold(value.Name.Local, "foreignObject") {
				return fmt.Errorf("SVG foreignObject content is not allowed")
			}
			if styleDepth == 0 && strings.EqualFold(value.Name.Local, "style") && svgElement {
				styleDepth = depth
				styleContent.Reset()
			}
			animationAttributes := make(map[string]string)
			for _, attribute := range value.Attr {
				name := strings.ToLower(attribute.Name.Local)
				text := strings.TrimSpace(attribute.Value)
				if attribute.Name.Space != "xmlns" && isSVGEventHandlerName(name) {
					return fmt.Errorf("SVG event handler attribute %q is not allowed", attribute.Name.Local)
				}
				if attribute.Name.Space == xmlNamespace && name == "base" && text != "" {
					return fmt.Errorf("SVG xml:base reference %q is not allowed", text)
				}
				isReference := svgElement && (attribute.Name.Space == "" && (name == "href" || name == "src") || attribute.Name.Space == xlinkNamespace && name == "href")
				if isReference && text != "" && !strings.HasPrefix(text, "#") {
					return fmt.Errorf("external SVG reference %q is not allowed", text)
				}
				if svgElement && attribute.Name.Space == "" {
					animationAttributes[name] = text
					if text != "" && svgAttributeMayReferenceCSSResource(name) {
						if err := validateLocalSVGStyle(text, name == "style"); err != nil {
							return err
						}
					}
				}
			}
			if svgElement && isSVGAnimationElement(value.Name.Local) {
				if err := validateSVGAnimationReferences(animationAttributes, namespaces); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if styleDepth == depth && strings.EqualFold(value.Name.Local, "style") && isSVGElement(value.Name, rootNamespace) {
				if err := validateLocalSVGStyle(styleContent.String(), true); err != nil {
					return err
				}
				styleDepth = 0
				styleContent.Reset()
			}
			depth--
			if len(namespaceStack) != 0 {
				namespaceStack = namespaceStack[:len(namespaceStack)-1]
			}
		case xml.CharData:
			if !seenRoot && len(value) != 0 {
				seenPreambleContent = true
			}
			tokenEnd := decoder.InputOffset()
			raw := data[int(tokenStart):int(tokenEnd)]
			if depth == 0 && !isXMLWhitespace(raw) {
				return fmt.Errorf("non-whitespace text outside SVG root element is not allowed")
			}
			if styleDepth != 0 {
				styleContent.Write(value)
			}
		case xml.Comment:
			if !seenRoot {
				seenPreambleContent = true
			}
		case xml.Directive:
			return fmt.Errorf("SVG XML directives are not allowed")
		case xml.ProcInst:
			if strings.EqualFold(value.Target, "xml") {
				if value.Target != "xml" || !xmlDeclarationPattern.Match(value.Inst) {
					return fmt.Errorf("SVG XML declaration is malformed")
				}
				if seenXMLDeclaration || seenPreambleContent || seenRoot {
					return fmt.Errorf("SVG XML declaration is misplaced or duplicated")
				}
				seenXMLDeclaration = true
				continue
			}
			if !seenRoot {
				seenPreambleContent = true
			}
			if strings.EqualFold(value.Target, "xml-stylesheet") {
				return fmt.Errorf("external SVG stylesheet is not allowed")
			}
		}
	}
	if !seenRoot {
		return fmt.Errorf("SVG root element is missing")
	}
	return nil
}

func isXMLWhitespace(value []byte) bool {
	for _, character := range value {
		switch character {
		case ' ', '\t', '\r', '\n':
		default:
			return false
		}
	}
	return true
}

func isSVGElement(name xml.Name, rootNamespace string) bool {
	return name.Space == svgNamespace || rootNamespace == "" && name.Space == ""
}

func isSVGAnimationElement(name string) bool {
	switch strings.ToLower(name) {
	case "animate", "set", "animatecolor", "animatetransform", "animatemotion":
		return true
	default:
		return false
	}
}

func validateSVGAnimationReferences(attributes map[string]string, namespaces map[string]string) error {
	target := resolveSVGAnimationAttributeName(attributes["attributename"], namespaces)
	if target == "" {
		return nil
	}
	if isSVGEventHandlerName(target) {
		return fmt.Errorf("SVG animation cannot set event handler attribute %q", target)
	}

	for _, valueName := range []string{"from", "to", "by", "values"} {
		value := attributes[valueName]
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := validateLocalSVGStyle(value, target == "style"); err != nil {
			return err
		}
		switch target {
		case "href", "src", "xlink:href":
			for _, candidate := range strings.Split(value, ";") {
				candidate = strings.TrimSpace(candidate)
				if candidate != "" && !strings.HasPrefix(candidate, "#") {
					return fmt.Errorf("SVG animation attribute %s references external resource %q", valueName, candidate)
				}
			}
		case "xml:base":
			return fmt.Errorf("SVG animation attribute %s sets xml:base", valueName)
		}
	}
	return nil
}

func resolveSVGAnimationAttributeName(value string, namespaces map[string]string) string {
	value = strings.TrimSpace(value)
	prefix, local, qualified := strings.Cut(value, ":")
	if !qualified {
		return strings.ToLower(value)
	}
	switch namespaces[prefix] {
	case xlinkNamespace:
		return "xlink:" + strings.ToLower(local)
	case xmlNamespace:
		return "xml:" + strings.ToLower(local)
	default:
		return strings.ToLower(value)
	}
}

func isSVGEventHandlerName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if _, local, qualified := strings.Cut(name, ":"); qualified {
		name = local
	}
	return strings.HasPrefix(name, "on")
}

func svgAttributeMayReferenceCSSResource(name string) bool {
	switch name {
	case "style", "fill", "stroke", "filter", "clip-path", "mask", "marker", "marker-start", "marker-mid", "marker-end", "cursor", "color-profile":
		return true
	default:
		return false
	}
}

func validateLocalSVGStyle(value string, rejectImports bool) error {
	_, err := rewriteCSSURLs([]byte(value), func(reference string) (string, error) {
		if reference != "" && !strings.HasPrefix(reference, "#") {
			return "", fmt.Errorf("external SVG CSS reference %q is not allowed", reference)
		}
		return reference, nil
	}, func(kind css.TokenType, token []byte) error {
		switch kind {
		case css.AtKeywordToken:
			if rejectImports && strings.EqualFold(unescapeCSS(string(token)), "@import") {
				return fmt.Errorf("external SVG stylesheet import is not allowed")
			}
		case css.BadURLToken:
			return fmt.Errorf("malformed SVG CSS url() reference")
		case css.URLToken:
			if !bytes.HasSuffix(bytes.TrimSpace(token), []byte(")")) {
				return fmt.Errorf("malformed SVG CSS url() reference")
			}
		}
		return nil
	})
	return err
}
