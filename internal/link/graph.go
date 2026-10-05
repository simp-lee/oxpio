package link

import (
	"net/url"
	"path"
	"sort"
	"strings"

	internalmarkdown "github.com/simp-lee/oxpio/internal/markdown"
	internalwikilink "github.com/simp-lee/oxpio/internal/markdown/wikilink"
	"github.com/simp-lee/oxpio/internal/model"
)

// BuildGraph derives a deterministic public note link graph from pass-2 resolved outlinks.
//
// The resolvedOutLinks map must be keyed by public source note path and contain the
// render-local outlinks produced during pass 2, including links merged onto the host
// page from embedded content. Link diagnostics are owned by resolver/render phases,
// so unresolved, self-referential, or non-public targets are ignored here.
func BuildGraph(idx *model.VaultIndex, resolvedOutLinks map[string][]model.LinkRef) *model.LinkGraph {
	graph := &model.LinkGraph{
		Forward:  map[string][]string{},
		Backward: map[string][]string{},
	}
	if idx == nil || len(idx.Notes) == 0 {
		return graph
	}

	publicPaths := sortedPublicPaths(idx.Notes)
	publicSet := make(map[string]struct{}, len(publicPaths))
	backwardSets := make(map[string]map[string]struct{}, len(publicPaths))
	for _, relPath := range publicPaths {
		graph.Forward[relPath] = []string{}
		graph.Backward[relPath] = []string{}
		publicSet[relPath] = struct{}{}
		backwardSets[relPath] = make(map[string]struct{})
	}

	for _, sourceRelPath := range publicPaths {
		outgoing := make(map[string]struct{})
		for i := range resolvedOutLinks[sourceRelPath] {
			targetRelPath := resolvedOutLinks[sourceRelPath][i].ResolvedRelPath
			if targetRelPath == "" {
				continue
			}
			if targetRelPath == sourceRelPath {
				continue
			}

			if _, ok := publicSet[targetRelPath]; !ok {
				continue
			}

			outgoing[targetRelPath] = struct{}{}
			backwardSets[targetRelPath][sourceRelPath] = struct{}{}
		}

		graph.Forward[sourceRelPath] = sortedMembers(outgoing)
	}

	for _, targetRelPath := range publicPaths {
		graph.Backward[targetRelPath] = sortedMembers(backwardSets[targetRelPath])
	}

	return graph
}

// BuildSourceGraph resolves only links and note embeds declared directly in
// each public note's source AST. It is independent from render-expanded links.
func BuildSourceGraph(idx *model.VaultIndex) *model.LinkGraph {
	graph := &model.LinkGraph{
		Forward:  map[string][]string{},
		Backward: map[string][]string{},
	}
	if idx == nil || len(idx.Notes) == 0 {
		return graph
	}

	publicPaths := sortedPublicPaths(idx.Notes)
	backwardSets := make(map[string]map[string]struct{}, len(publicPaths))
	for _, relPath := range publicPaths {
		graph.Forward[relPath] = []string{}
		graph.Backward[relPath] = []string{}
		backwardSets[relPath] = make(map[string]struct{})
	}

	for _, sourcePath := range publicPaths {
		source := idx.Notes[sourcePath]
		outgoing := make(map[string]struct{})
		if source != nil {
			for _, ref := range source.OutLinks {
				rawTarget := ref.RawTarget
				fragment := ref.Fragment
				if ref.Standard {
					rawTarget = internalmarkdown.NormalizeDestination(rawTarget)
					// Derive both semantic components after entity and escape
					// normalization; the raw '#' may belong to an entity.
					fragment = ""
				}
				target, fragment := sourceLinkTarget(rawTarget, fragment)
				addResolvedSourceTarget(outgoing, idx, source, target, fragment, ref.Standard)
			}
			for _, embed := range source.Embeds {
				if embed.IsImage {
					continue
				}
				fragment := embed.Fragment
				if strings.HasPrefix(strings.TrimSpace(fragment), "^") {
					fragment = ""
				}
				addResolvedSourceTarget(outgoing, idx, source, embed.Target, fragment, false)
			}
		}

		delete(outgoing, sourcePath)
		graph.Forward[sourcePath] = sortedMembers(outgoing)
		for targetPath := range outgoing {
			backwardSets[targetPath][sourcePath] = struct{}{}
		}
	}

	for _, targetPath := range publicPaths {
		graph.Backward[targetPath] = sortedMembers(backwardSets[targetPath])
	}
	return graph
}

func addResolvedSourceTarget(targets map[string]struct{}, idx *model.VaultIndex, source *model.Note, target string, fragment string, standard bool) {
	if targets == nil || idx == nil || source == nil {
		return
	}
	target = strings.TrimSpace(target)
	parsed, parseErr := url.Parse(target)
	if standard && parseErr == nil {
		if parsed.IsAbs() || parsed.Host != "" {
			return
		}
		if parsed.Fragment != "" && strings.TrimSpace(fragment) == "" {
			fragment = parsed.Fragment
		}
		target = parsed.Path
	}
	// Only ordinary Markdown links are source-relative. Wikilinks and note
	// embeds must retain LookupTarget's vault-path and basename semantics.
	pathTarget := standard && target != ""
	if pathTarget {
		if standard && parseErr == nil && strings.HasPrefix(target, "/") {
			routeLookup := internalwikilink.LookupRouteTarget(idx, source, parsed.EscapedPath(), strings.TrimSpace(fragment))
			if routeLookup.Note != nil || routeLookup.Section != nil {
				if routeLookup.Note != nil && !routeLookup.MissingFragment {
					targets[routeLookup.Note.RelPath] = struct{}{}
				}
				return
			}
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join(path.Dir(source.RelPath), target)
		}
	}
	lookup := internalwikilink.LookupTarget(idx, source, target, strings.TrimSpace(fragment))
	if pathTarget {
		lookup = internalwikilink.LookupPathTarget(idx, source, target, strings.TrimSpace(fragment))
		if lookup.Note == nil && lookup.Section == nil && standard && parseErr == nil && source.Route != "" {
			if base, err := url.Parse(source.Route); err == nil {
				resolved := base.ResolveReference(parsed)
				routeLookup := internalwikilink.LookupRouteTarget(idx, source, resolved.EscapedPath(), strings.TrimSpace(fragment))
				if routeLookup.Note != nil || routeLookup.Section != nil {
					if routeLookup.Note != nil && !routeLookup.MissingFragment {
						targets[routeLookup.Note.RelPath] = struct{}{}
					}
					return
				}
			}
		}
	}
	if lookup.Note == nil || lookup.Unpublished || lookup.MissingFragment {
		return
	}
	if idx.Notes[lookup.Note.RelPath] == nil {
		return
	}
	targets[lookup.Note.RelPath] = struct{}{}
}

func sourceLinkTarget(rawTarget string, fragment string) (string, string) {
	rawTarget = strings.TrimSpace(rawTarget)
	fragment = strings.TrimSpace(fragment)
	if fragment != "" {
		if target, ok := strings.CutSuffix(rawTarget, "#"+fragment); ok {
			if decoded, err := url.PathUnescape(fragment); err == nil {
				fragment = decoded
			}
			return strings.TrimSpace(target), fragment
		}
		return rawTarget, fragment
	}
	if target, parsedFragment, ok := strings.Cut(rawTarget, "#"); ok {
		parsedFragment = strings.TrimSpace(parsedFragment)
		if decoded, err := url.PathUnescape(parsedFragment); err == nil {
			parsedFragment = decoded
		}
		return strings.TrimSpace(target), parsedFragment
	}
	return rawTarget, ""
}

func sortedPublicPaths(notes map[string]*model.Note) []string {
	paths := make([]string, 0, len(notes))
	for relPath := range notes {
		paths = append(paths, relPath)
	}
	sort.Strings(paths)
	return paths
}

func sortedMembers(values map[string]struct{}) []string {
	if len(values) == 0 {
		return []string{}
	}

	members := make([]string, 0, len(values))
	for value := range values {
		members = append(members, value)
	}
	sort.Strings(members)
	return members
}
