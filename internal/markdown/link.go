package markdown

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/markdown/pathutil"
	internalwikilink "github.com/simp-lee/oxpio/internal/markdown/wikilink"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/resourcepath"
	"github.com/simp-lee/oxpio/internal/slug"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

type strictLinkExtender struct {
	index            *model.VaultIndex
	sourceNote       *model.Note
	outputNote       *model.Note
	headingIDPrefix  string
	assetSink        AssetSink
	diagnostics      *diag.Collector
	linkResolver     *internalwikilink.VaultResolver
	publicPageRoutes map[string]struct{}
}

func (e strictLinkExtender) Extend(markdown goldmark.Markdown) {
	markdown.Renderer().AddOptions(renderer.WithNodeRenderers(util.Prioritized(&strictLinkRenderer{
		Config: gmhtml.NewConfig(), index: e.index, sourceNote: e.sourceNote, outputNote: e.outputNote, headingIDPrefix: e.headingIDPrefix, assetSink: e.assetSink, diagnostics: e.diagnostics, linkResolver: e.linkResolver, publicPageRoutes: e.publicPageRoutes,
	}, 499)))
}

type strictLinkRenderer struct {
	gmhtml.Config
	index            *model.VaultIndex
	sourceNote       *model.Note
	outputNote       *model.Note
	headingIDPrefix  string
	assetSink        AssetSink
	diagnostics      *diag.Collector
	linkResolver     *internalwikilink.VaultResolver
	publicPageRoutes map[string]struct{}
}

func (r *strictLinkRenderer) RegisterFuncs(register renderer.NodeRendererFuncRegisterer) {
	register.Register(gast.KindLink, r.renderLink)
}

func (r *strictLinkRenderer) renderLink(w util.BufWriter, source []byte, node gast.Node, entering bool) (gast.WalkStatus, error) {
	link := node.(*gast.Link)
	if entering {
		line := lineForLink(source, link)
		if r.sourceNote != nil && r.sourceNote.BodyStartLine > 1 {
			line += r.sourceNote.BodyStartLine - 1
		}
		rawDestination := strings.TrimSpace(string(link.Destination))
		destination := r.rewriteDestination(NormalizeDestination(rawDestination), rawDestination, line)
		_, _ = w.WriteString(`<a href="`)
		escaped := util.URLEscape([]byte(destination), false)
		if r.Unsafe || !gmhtml.IsDangerousURL(escaped) {
			_, _ = w.Write(util.EscapeHTML(escaped))
		}
		_ = w.WriteByte('"')
		if len(link.Title) > 0 {
			_, _ = w.WriteString(` title="`)
			r.Writer.Write(w, link.Title)
			_ = w.WriteByte('"')
		}
		_ = w.WriteByte('>')
		return gast.WalkContinue, nil
	}
	_, _ = w.WriteString(`</a>`)
	return gast.WalkContinue, nil
}

func (r *strictLinkRenderer) rewriteDestination(raw string, sourceTarget string, line int) string {
	if r == nil || r.index == nil || r.sourceNote == nil || raw == "" {
		return raw
	}
	sourceTarget = strings.TrimSpace(sourceTarget)
	if sourceTarget == "" {
		sourceTarget = raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		r.recordUnresolvedLocalAttachment(raw, sourceTarget, line)
		return raw
	}
	if parsed.IsAbs() || parsed.Host != "" || strings.HasPrefix(raw, "//") {
		r.recordUnresolvedLocalAttachment(raw, sourceTarget, line)
		return raw
	}
	escapedTargetPath := parsed.EscapedPath()
	targetPath, err := url.PathUnescape(escapedTargetPath)
	if err != nil {
		r.recordUnresolvedLocalAttachment(raw, sourceTarget, line)
		return raw
	}
	fragment := parsed.Fragment
	if targetPath == "" && fragment == "" {
		return raw
	}
	rootRelative := targetPath != "" && strings.HasPrefix(targetPath, "/")
	vaultPath := path.Clean(path.Join(path.Dir(r.sourceNote.RelPath), targetPath))
	if targetPath == "" {
		vaultPath = ""
	}
	lookup := internalwikilink.LookupResult{}
	publicPageRoute := ""
	if rootRelative {
		publicPageRoute = escapedTargetPath
		lookup = internalwikilink.LookupRouteTarget(r.index, r.sourceNote, escapedTargetPath, fragment)
		if lookup.Note == nil && lookup.Section == nil && !lookup.MissingFragment {
			lookup = internalwikilink.LookupPathTarget(r.index, r.sourceNote, strings.TrimPrefix(targetPath, "/"), fragment)
		}
	} else if targetPath != "" {
		lookup = internalwikilink.LookupPathTarget(r.index, r.sourceNote, vaultPath, fragment)
		if lookup.Note == nil && lookup.Section == nil && r.sourceNote.Route != "" {
			if base, parseErr := url.Parse(r.sourceNote.Route); parseErr == nil {
				resolved := base.ResolveReference(parsed)
				publicPageRoute = resolved.EscapedPath()
				lookup = internalwikilink.LookupRouteTarget(r.index, r.sourceNote, publicPageRoute, fragment)
			}
		}
	} else {
		lookup = internalwikilink.LookupTarget(r.index, r.sourceNote, "", fragment)
	}
	if lookup.Note == nil {
		section := lookup.Section
		if section != nil && inLinkVersionScope(r.sourceNote, section) {
			resolvedFragment := ""
			if fragment != "" {
				if lookup.MissingFragment || lookup.FragmentID == "" {
					resolvedFragment = fragment
					if r.diagnostics != nil {
						r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityWarning, Kind: diag.KindDeadLink, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown link %q targets a missing section heading", sourceTarget)})
					}
				} else {
					resolvedFragment = lookup.FragmentID
				}
			}
			href := internalwikilink.BuildSectionHref(r.outputNote, r.sourceNote, section, resolvedFragment, r.headingIDPrefix)
			if rootRelative {
				href = strings.TrimSuffix(r.outputNote.BasePath, "/") + section.Route
				if resolvedFragment != "" {
					href += "#" + resolvedFragment
				}
			}
			if parsed.RawQuery != "" || parsed.ForceQuery {
				if fragmentIndex := strings.IndexByte(href, '#'); fragmentIndex >= 0 {
					href = href[:fragmentIndex] + "?" + parsed.RawQuery + href[fragmentIndex:]
				} else {
					href += "?" + parsed.RawQuery
				}
			}
			if !lookup.MissingFragment && r.linkResolver != nil {
				embeddedFragment := resolvedFragment
				if rootRelative {
					embeddedFragment = ""
				}
				r.linkResolver.MarkStandardSectionLinkResolved(standardLinkLedgerTarget(sourceTarget), section, embeddedFragment)
			}
			return href
		}
		if canonicalRoute, ok := lookupPublicPageRoute(r.publicPageRoutes, publicPageRoute); fragment == "" && ok {
			return publicPageHref(r.outputNote, canonicalRoute, parsed, rootRelative)
		}
		resourceLookup := resourcepath.LookupPath(r.sourceNote, r.index.AttachmentFolderPath, escapedTargetPath, r.index.LookupResourcePath)
		if resource := resourceLookup.Path; resource != "" {
			if resourcepath.IsResourceAllowedForNote(r.index, r.sourceNote, resource) {
				destination := resource
				if r.assetSink != nil {
					if planned := r.assetSink.Register(resource); planned != "" {
						destination = planned
					}
				}
				suffix := ""
				if parsed.RawQuery != "" {
					suffix += "?" + parsed.RawQuery
				}
				if escapedFragment := parsed.EscapedFragment(); escapedFragment != "" {
					suffix += "#" + escapedFragment
				}
				return pathutil.RelativeToNoteOutput(r.outputNote, destination) + suffix
			}
			if r.diagnostics != nil {
				r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityError, Kind: diag.KindUnresolvedAsset, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown attachment %q is outside the current version resource scope", sourceTarget)})
			}
			if rootRelative {
				return prefixRootRelativeDestination(r.outputNote, raw)
			}
			return raw
		}
		if len(resourceLookup.Ambiguous) > 0 {
			if r.diagnostics != nil {
				r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityError, Kind: diag.KindUnresolvedAsset, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown attachment %q matched multiple publishable vault assets after canonical path normalization (%s); refusing canonical fallback", sourceTarget, strings.Join(resourceLookup.Ambiguous, ", "))})
			}
			if rootRelative {
				return prefixRootRelativeDestination(r.outputNote, raw)
			}
			return raw
		}
		attachment := isMarkdownAttachmentTarget(targetPath)
		if !attachment {
			if r.diagnostics != nil {
				r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityWarning, Kind: diag.KindDeadLink, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown link %q could not be resolved", sourceTarget)})
			}
		} else if targetPath != "" && r.diagnostics != nil {
			r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityError, Kind: diag.KindUnresolvedAsset, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown attachment %q could not be resolved", sourceTarget)})
		}
		if rootRelative {
			return prefixRootRelativeDestination(r.outputNote, raw)
		}
		return raw
	}
	if lookup.Unpublished {
		if r.diagnostics != nil {
			r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityWarning, Kind: diag.KindDeadLink, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown link %q targets unpublished content", sourceTarget)})
		}
		return prefixRootRelativeDestination(r.outputNote, raw)
	}
	if lookup.MissingFragment {
		if r.diagnostics != nil {
			r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityWarning, Kind: diag.KindDeadLink, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown link %q targets a missing fragment", sourceTarget)})
		}
		return prefixRootRelativeDestination(r.outputNote, raw)
	}
	if r.linkResolver != nil {
		embeddedFragment := lookup.FragmentID
		if rootRelative {
			embeddedFragment = ""
		}
		r.linkResolver.MarkStandardLinkResolved(standardLinkLedgerTarget(sourceTarget), lookup.Note, embeddedFragment)
	}
	href := internalwikilink.BuildNoteHref(r.outputNote, r.sourceNote, lookup.Note, lookup.FragmentID, r.headingIDPrefix)
	if rootRelative {
		href = strings.TrimSuffix(r.outputNote.BasePath, "/") + lookup.Note.Route
		if lookup.FragmentID != "" {
			href += "#" + lookup.FragmentID
		}
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		if fragmentIndex := strings.IndexByte(href, '#'); fragmentIndex >= 0 {
			href = href[:fragmentIndex] + "?" + parsed.RawQuery + href[fragmentIndex:]
		} else {
			href += "?" + parsed.RawQuery
		}
	}
	return href
}

func (r *strictLinkRenderer) recordUnresolvedLocalAttachment(destination string, sourceTarget string, line int) {
	if r == nil || r.diagnostics == nil || !resourcepath.IsLocalTarget(destination) {
		return
	}
	r.diagnostics.Add(diag.Diagnostic{Severity: diag.SeverityError, Kind: diag.KindUnresolvedAsset, Location: diag.Location{Path: r.sourceNote.RelPath, Line: line}, Target: sourceTarget, Message: fmt.Sprintf("markdown attachment %q could not be resolved", sourceTarget)})
}

func standardLinkLedgerTarget(raw string) string {
	return strings.TrimSpace(raw)
}

func isMarkdownAttachmentTarget(targetPath string) bool {
	extension := strings.ToLower(path.Ext(strings.TrimSpace(targetPath)))
	return extension != "" && extension != ".md"
}

func lookupPublicPageRoute(routes map[string]struct{}, route string) (string, bool) {
	if route == "" {
		return "", false
	}
	canonical := slug.CanonicalRoute(route)
	if _, ok := routes[canonical]; ok {
		return canonical, true
	}
	if canonical == "/" || strings.HasSuffix(canonical, "/") {
		return canonical, false
	}
	canonical += "/"
	_, ok := routes[canonical]
	return canonical, ok
}

func publicPageHref(note *model.Note, route string, parsed *url.URL, rootRelative bool) string {
	href := route
	if rootRelative {
		if note != nil && note.BasePath != "" {
			href = strings.TrimSuffix(note.BasePath, "/") + route
		}
	} else {
		href = pathutil.RelativeToNoteOutput(note, route)
		if strings.HasSuffix(route, "/") {
			if href == "" || href == "." {
				href = "./"
			} else if !strings.HasSuffix(href, "/") {
				href += "/"
			}
		}
	}
	if parsed != nil && (parsed.RawQuery != "" || parsed.ForceQuery) {
		href += "?" + parsed.RawQuery
	}
	return href
}

func inLinkVersionScope(note *model.Note, section *model.Section) bool {
	return note == nil || section == nil || section.VersionID == "" || note.VersionID == section.VersionID
}

func prefixRootRelativeDestination(note *model.Note, raw string) string {
	if note == nil || note.BasePath == "" || raw == "" || !strings.HasPrefix(raw, "/") {
		return raw
	}
	cut := strings.IndexAny(raw, "?#")
	if cut < 0 {
		cut = len(raw)
	}
	return strings.TrimSuffix(note.BasePath, "/") + raw[:cut] + raw[cut:]
}

// NormalizeDestination applies Goldmark's Markdown escape, entity, and URI
// escaping rules before a destination is parsed or looked up.
func NormalizeDestination(value string) string {
	normalized := util.URLEscape([]byte(strings.TrimSpace(value)), true)
	var escaped strings.Builder
	for index := 0; index < len(normalized); index++ {
		if normalized[index] != '%' || index+2 < len(normalized) && isHexDigit(normalized[index+1]) && isHexDigit(normalized[index+2]) {
			escaped.WriteByte(normalized[index])
			continue
		}
		escaped.WriteString("%25")
	}
	return escaped.String()
}

func isHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func lineForLink(source []byte, link *gast.Link) int {
	if link == nil {
		return 0
	}
	position := link.Pos()
	if position < 0 || position > len(source) {
		return 0
	}
	return 1 + strings.Count(string(source[:position]), "\n")
}
