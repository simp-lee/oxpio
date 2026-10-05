package wikilink

import (
	"reflect"
	"testing"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/model"
	gmwikilink "go.abhg.dev/goldmark/wikilink"
)

func TestVaultResolverResolveWikilink_BestMatch(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	exact := testNote("projects/deep/guide.md", "projects/deep/guide")
	filename := testNote("misc/reference.md", "misc/reference")
	alias := testNote("docs/handbook.md", "docs/handbook", withAliases("Team Docs"))
	near := testNote("notes/guide.md", "notes/guide")
	far := testNote("archive/guide.md", "archive/guide")

	current.OutLinks = []model.LinkRef{
		{RawTarget: "projects/deep/guide", Line: 3},
		{RawTarget: "Reference", Line: 4},
		{RawTarget: "Team Docs", Line: 5},
		{RawTarget: "Guide", Line: 6},
	}

	idx := buildIndex([]*model.Note{current, exact, filename, alias, near, far}, nil)
	resolver := NewRenderVaultResolver(idx, current, current, "", diag.NewCollector())

	tests := []struct {
		name       string
		node       *gmwikilink.Node
		want       string
		wantTarget string
	}{
		{
			name:       "exact path",
			node:       &gmwikilink.Node{Target: []byte("projects/deep/guide")},
			want:       "../../projects/deep/guide/",
			wantTarget: exact.RelPath,
		},
		{
			name:       "unique filename",
			node:       &gmwikilink.Node{Target: []byte("Reference")},
			want:       "../../misc/reference/",
			wantTarget: filename.RelPath,
		},
		{
			name:       "unique alias",
			node:       &gmwikilink.Node{Target: []byte("Team Docs")},
			want:       "../../docs/handbook/",
			wantTarget: alias.RelPath,
		},
		{
			name:       "nearest path disambiguation",
			node:       &gmwikilink.Node{Target: []byte("Guide")},
			want:       "../guide/",
			wantTarget: near.RelPath,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.ResolveWikilink(tt.node)
			if err != nil {
				t.Fatalf("ResolveWikilink() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("ResolveWikilink() = %q, want %q", string(got), tt.want)
			}
			gotOutLinks := resolver.OutLinks()
			if gotOutLinks[i].ResolvedRelPath != tt.wantTarget {
				t.Fatalf("resolver.OutLinks()[%d].ResolvedRelPath = %q, want %q", i, gotOutLinks[i].ResolvedRelPath, tt.wantTarget)
			}
		})
	}
	for i := range current.OutLinks {
		if current.OutLinks[i].ResolvedRelPath != "" {
			t.Fatalf("current.OutLinks[%d].ResolvedRelPath = %q, want source note to remain unchanged", i, current.OutLinks[i].ResolvedRelPath)
		}
	}
}

func TestVaultResolverResolveWikilink_UnpublishedDowngradesToPlainText(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "Private", Line: 12}}

	private := testNote("private/secret.md", "private/secret", withAliases("Private"))
	idx := buildIndex([]*model.Note{current}, []*model.Note{private})
	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(idx, current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("Private")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveWikilink() = %q, want nil destination", string(got))
	}
	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("resolver.OutLinks() = %#v, want one unresolved outlink", gotOutLinks)
	}
	if current.OutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("ResolvedRelPath = %q, want source note to remain unchanged", current.OutLinks[0].ResolvedRelPath)
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     kindUnpublishedWikilink,
		Location: diag.Location{Path: current.RelPath, Line: 12},
		Target:   "Private",
		Message:  `wikilink "Private" points to unpublished note "private/secret.md"; rendering as plain text`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestVaultResolverResolveWikilink_ExplicitPathDoesNotFallbackToBasename(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "private/secret", Line: 14}}

	publicCollision := testNote("docs/secret.md", "docs/secret")
	unpublished := testNote("private/secret.md", "private/secret")
	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current, publicCollision}, []*model.Note{unpublished}), current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("private/secret")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveWikilink() = %q, want nil destination", string(got))
	}
	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("resolver.OutLinks() = %#v, want one unresolved outlink", gotOutLinks)
	}
	if current.OutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("ResolvedRelPath = %q, want source note to remain unchanged", current.OutLinks[0].ResolvedRelPath)
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     kindUnpublishedWikilink,
		Location: diag.Location{Path: current.RelPath, Line: 14},
		Target:   "private/secret",
		Message:  `wikilink "private/secret" points to unpublished note "private/secret.md"; rendering as plain text`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestVaultResolverResolveWikilink_DeadLinkWarns(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "Missing", Line: 18}}

	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current}, nil), current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("Missing")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveWikilink() = %q, want nil destination", string(got))
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     diag.KindDeadLink,
		Location: diag.Location{Path: current.RelPath, Line: 18},
		Target:   "Missing",
		Message:  `wikilink "Missing" could not be resolved`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestVaultResolverResolveWikilink_AmbiguityWarnsAndChoosesLexicographicFirst(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "Docs", Line: 9}}

	alpha := testNote("alpha/docs.md", "alpha/docs", withAliases("Docs"))
	beta := testNote("beta/docs.md", "beta/docs", withAliases("Docs"))
	idx := buildIndex([]*model.Note{current, alpha, beta}, nil)
	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(idx, current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("Docs")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if string(got) != "../../alpha/docs/" {
		t.Fatalf("ResolveWikilink() = %q, want %q", string(got), "../../alpha/docs/")
	}
	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != alpha.RelPath {
		t.Fatalf("resolver.OutLinks() = %#v, want chosen alpha target", gotOutLinks)
	}
	if current.OutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("ResolvedRelPath = %q, want source note to remain unchanged", current.OutLinks[0].ResolvedRelPath)
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     kindAmbiguousWikilink,
		Location: diag.Location{Path: current.RelPath, Line: 9},
		Target:   "Docs",
		Message:  `wikilink "Docs" matched multiple notes at the same path distance (alpha/docs.md, beta/docs.md); choosing "alpha/docs.md"`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestVaultResolverResolveWikilink_UniqueAliasBeatsDuplicateFilenameMatches(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "Overview", Line: 16}}

	nearFilename := testNote("notes/overview.md", "notes/overview")
	farFilename := testNote("archive/overview.md", "archive/overview")
	alias := testNote("docs/home.md", "docs/home", withAliases("Overview"))
	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current, nearFilename, farFilename, alias}, nil), current, current, "", diag.NewCollector())

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("Overview")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if string(got) != "../../docs/home/" {
		t.Fatalf("ResolveWikilink() = %q, want %q", string(got), "../../docs/home/")
	}
	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != alias.RelPath {
		t.Fatalf("resolver.OutLinks() = %#v, want alias target", gotOutLinks)
	}
	if current.OutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("ResolvedRelPath = %q, want source note to remain unchanged", current.OutLinks[0].ResolvedRelPath)
	}
}

func TestVaultResolverResolveWikilink_DottedTargetsStillUseBestMatch(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	filename := testNote("docs/Release v1.0.md", "docs/release-v1-0")
	alias := testNote("docs/team-docs.md", "docs/team-docs", withAliases("Team Docs v2.1"))
	current.OutLinks = []model.LinkRef{
		{RawTarget: "Release v1.0", Line: 22},
		{RawTarget: "Team Docs v2.1", Line: 23},
	}

	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current, filename, alias}, nil), current, current, "", diag.NewCollector())

	tests := []struct {
		name       string
		node       *gmwikilink.Node
		want       string
		wantTarget string
	}{
		{
			name:       "dotted filename",
			node:       &gmwikilink.Node{Target: []byte("Release v1.0")},
			want:       "../../docs/release-v1-0/",
			wantTarget: filename.RelPath,
		},
		{
			name:       "dotted alias",
			node:       &gmwikilink.Node{Target: []byte("Team Docs v2.1")},
			want:       "../../docs/team-docs/",
			wantTarget: alias.RelPath,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.ResolveWikilink(tt.node)
			if err != nil {
				t.Fatalf("ResolveWikilink() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("ResolveWikilink() = %q, want %q", string(got), tt.want)
			}
			gotOutLinks := resolver.OutLinks()
			if gotOutLinks[i].ResolvedRelPath != tt.wantTarget {
				t.Fatalf("resolver.OutLinks()[%d].ResolvedRelPath = %q, want %q", i, gotOutLinks[i].ResolvedRelPath, tt.wantTarget)
			}
		})
	}
	for i := range current.OutLinks {
		if current.OutLinks[i].ResolvedRelPath != "" {
			t.Fatalf("current.OutLinks[%d].ResolvedRelPath = %q, want source note to remain unchanged", i, current.OutLinks[i].ResolvedRelPath)
		}
	}
}

func TestLookupTargetCanonicalizesUnicodeLookupKeys(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	publicByName := testNote("notes/Cafe\u0301 Guide.md", "notes/cafe-guide")
	publicByAlias := testNote("notes/alias.md", "notes/alias", withAliases("Re\u0301sume\u0301"))
	unpublishedByName := testNote("notes/De\u0301ja\u0300 Vu.md", "notes/deja-vu")
	unpublishedByAlias := testNote("notes/private.md", "notes/private", withAliases("Touche\u0301"))

	idx := buildIndex(
		[]*model.Note{current, publicByName, publicByAlias},
		[]*model.Note{unpublishedByName, unpublishedByAlias},
	)

	tests := []struct {
		name            string
		target          string
		want            *model.Note
		wantUnpublished bool
	}{
		{name: "public filename", target: "Café Guide", want: publicByName},
		{name: "public alias", target: "Résumé", want: publicByAlias},
		{name: "unpublished filename", target: "Déjà Vu", want: unpublishedByName, wantUnpublished: true},
		{name: "unpublished alias", target: "Touché", want: unpublishedByAlias, wantUnpublished: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := LookupTarget(idx, current, tt.target, "")
			if lookup.Note != tt.want {
				t.Fatalf("LookupTarget(%q).Note = %#v, want %#v", tt.target, lookup.Note, tt.want)
			}
			if lookup.Unpublished != tt.wantUnpublished {
				t.Fatalf("LookupTarget(%q).Unpublished = %v, want %v", tt.target, lookup.Unpublished, tt.wantUnpublished)
			}
			if lookup.MissingFragment {
				t.Fatalf("LookupTarget(%q).MissingFragment = true, want false", tt.target)
			}
			if len(lookup.Ambiguous) != 0 {
				t.Fatalf("LookupTarget(%q).Ambiguous = %#v, want nil", tt.target, lookup.Ambiguous)
			}
		})
	}
}

func TestVaultResolverResolveWikilink_ExplicitPathCanonicalizesUnicodePublicMatch(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "notes/Café Guide", Line: 27}}

	target := testNote("notes/Cafe\u0301 Guide.md", "notes/cafe-guide")
	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current, target}, nil), current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("notes/Café Guide")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if string(got) != "../cafe-guide/" {
		t.Fatalf("ResolveWikilink() = %q, want %q", string(got), "../cafe-guide/")
	}

	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != target.RelPath {
		t.Fatalf("resolver.OutLinks() = %#v, want resolved explicit-path outlink", gotOutLinks)
	}
	if current.OutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("ResolvedRelPath = %q, want source note to remain unchanged", current.OutLinks[0].ResolvedRelPath)
	}
	if got := collector.Diagnostics(); len(got) != 0 {
		t.Fatalf("collector.Diagnostics() = %#v, want no diagnostics", got)
	}
}

func TestVaultResolverResolveWikilink_ExplicitPathCanonicalizesUnicodeUnpublishedDowngrade(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "private/Café Guide", Line: 29}}

	unpublished := testNote("private/Cafe\u0301 Guide.md", "private/cafe-guide")
	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current}, []*model.Note{unpublished}), current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("private/Café Guide")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveWikilink() = %q, want nil destination", string(got))
	}

	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("resolver.OutLinks() = %#v, want one unresolved outlink", gotOutLinks)
	}
	if current.OutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("ResolvedRelPath = %q, want source note to remain unchanged", current.OutLinks[0].ResolvedRelPath)
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     kindUnpublishedWikilink,
		Location: diag.Location{Path: current.RelPath, Line: 29},
		Target:   "private/Café Guide",
		Message:  `wikilink "private/Café Guide" points to unpublished note "` + unpublished.RelPath + `"; rendering as plain text`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestVaultResolverResolveWikilink_CanvasLookupCanonicalizesUnicodeResourceNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
	}{
		{name: "explicit path", target: "boards/Café.canvas"},
		{name: "basename fallback", target: "Café.canvas"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			current := testNote("notes/current.md", "notes/current")
			current.OutLinks = []model.LinkRef{{RawTarget: tt.target, Line: 15}}

			idx := buildIndex([]*model.Note{current}, nil)
			idx.SetResources([]string{"boards/Cafe\u0301.canvas"})

			collector := diag.NewCollector()
			resolver := NewRenderVaultResolver(idx, current, current, "", collector)

			got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte(tt.target)})
			if err != nil {
				t.Fatalf("ResolveWikilink() error = %v", err)
			}
			if got != nil {
				t.Fatalf("ResolveWikilink() = %q, want nil destination", string(got))
			}

			gotOutLinks := resolver.OutLinks()
			if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != "" {
				t.Fatalf("resolver.OutLinks() = %#v, want one unresolved outlink", gotOutLinks)
			}

			want := []diag.Diagnostic{{
				Severity: diag.SeverityWarning,
				Kind:     diag.KindUnsupportedSyntax,
				Location: diag.Location{Path: current.RelPath, Line: 15},
				Target:   tt.target,
				Message:  `wikilink "` + tt.target + `" targets unsupported canvas content; rendering as plain text`,
			}}
			if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
				t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestVaultResolverResolveWikilink_RefusesAmbiguousCanvasBasenameFallback(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "plan.canvas", Line: 15}}

	idx := buildIndex([]*model.Note{current}, nil)
	idx.SetResources([]string{"boards/plan.canvas", "archive/plan.canvas"})

	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(idx, current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("plan.canvas")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveWikilink() = %q, want nil destination", string(got))
	}

	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("resolver.OutLinks() = %#v, want one unresolved outlink", gotOutLinks)
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     diag.KindUnsupportedSyntax,
		Location: diag.Location{Path: current.RelPath, Line: 15},
		Target:   "plan.canvas",
		Message:  `wikilink "plan.canvas" matched multiple canvas resources after canonical lookup (archive/plan.canvas, boards/plan.canvas); refusing canonical fallback and rendering as plain text`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestVaultResolverResolveWikilink_ResolvesFragments(t *testing.T) {
	t.Parallel()

	current := testNote(
		"notes/current.md",
		"notes/current",
		withHeadings(model.Heading{Level: 2, Text: "Current Section", ID: "current-section"}),
	)
	target := testNote(
		"guides/guide.md",
		"guides/guide",
		withHeadings(model.Heading{Level: 2, Text: "Section Title", ID: "section-title"}),
	)
	current.OutLinks = []model.LinkRef{
		{RawTarget: "Guide#Section Title", Line: 7},
		{RawTarget: "#Current Section", Line: 8},
	}

	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current, target}, nil), current, current, "", diag.NewCollector())

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("Guide"), Fragment: []byte("Section Title")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if string(got) != "../../guides/guide/#section-title" {
		t.Fatalf("ResolveWikilink() = %q, want %q", string(got), "../../guides/guide/#section-title")
	}
	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 2 || gotOutLinks[0].ResolvedRelPath != target.RelPath {
		t.Fatalf("resolver.OutLinks() = %#v, want first outlink resolved to target", gotOutLinks)
	}

	got, err = resolver.ResolveWikilink(&gmwikilink.Node{Fragment: []byte("Current Section")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if string(got) != "#current-section" {
		t.Fatalf("ResolveWikilink() = %q, want %q", string(got), "#current-section")
	}
	gotOutLinks = resolver.OutLinks()
	if gotOutLinks[1].ResolvedRelPath != current.RelPath {
		t.Fatalf("resolver.OutLinks()[1].ResolvedRelPath = %q, want %q", gotOutLinks[1].ResolvedRelPath, current.RelPath)
	}
	for i := range current.OutLinks {
		if current.OutLinks[i].ResolvedRelPath != "" {
			t.Fatalf("current.OutLinks[%d].ResolvedRelPath = %q, want source note to remain unchanged", i, current.OutLinks[i].ResolvedRelPath)
		}
	}
}

func TestVaultResolverResolveWikilink_MissingFragmentWarnsAndDoesNotResolve(t *testing.T) {
	t.Parallel()

	current := testNote("notes/current.md", "notes/current")
	current.OutLinks = []model.LinkRef{{RawTarget: "Guide#Missing Heading", Line: 21}}
	target := testNote(
		"guides/guide.md",
		"guides/guide",
		withHeadings(model.Heading{Level: 2, Text: "Section Title", ID: "section-title"}),
	)
	collector := diag.NewCollector()
	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{current, target}, nil), current, current, "", collector)

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("Guide"), Fragment: []byte("Missing Heading")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if got != nil {
		t.Fatalf("ResolveWikilink() = %q, want nil destination", string(got))
	}
	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 1 || gotOutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("resolver.OutLinks() = %#v, want one unresolved outlink", gotOutLinks)
	}
	if current.OutLinks[0].ResolvedRelPath != "" {
		t.Fatalf("ResolvedRelPath = %q, want source note to remain unchanged", current.OutLinks[0].ResolvedRelPath)
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     diag.KindDeadLink,
		Location: diag.Location{Path: current.RelPath, Line: 21},
		Target:   "Guide#Missing Heading",
		Message:  `wikilink "Guide#Missing Heading" points to missing heading "Missing Heading" in "guides/guide.md"`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestNewRenderVaultResolverUsesHostOutputAndNamespacedSelfFragments(t *testing.T) {
	t.Parallel()

	host := testNote("notes/posts/host.md", "posts/2024/host")
	source := testNote(
		"guides/topics/nested/guide.md",
		"guides/topics/nested/guide",
		withHeadings(model.Heading{Level: 2, Text: "Intro", ID: "intro"}),
	)
	target := testNote("library/reference.md", "reference/deep-dive")
	source.OutLinks = []model.LinkRef{
		{RawTarget: "Reference", Line: 3},
		{RawTarget: "#Intro", Line: 5},
	}

	resolver := NewRenderVaultResolver(buildIndex([]*model.Note{host, source, target}, nil), source, host, "embed-2-", diag.NewCollector())

	got, err := resolver.ResolveWikilink(&gmwikilink.Node{Target: []byte("Reference")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if string(got) != "../../../reference/deep-dive/" {
		t.Fatalf("ResolveWikilink() = %q, want %q", string(got), "../../../reference/deep-dive/")
	}
	gotOutLinks := resolver.OutLinks()
	if len(gotOutLinks) != 2 || gotOutLinks[0].ResolvedRelPath != target.RelPath {
		t.Fatalf("resolver.OutLinks() = %#v, want first outlink resolved to target", gotOutLinks)
	}

	got, err = resolver.ResolveWikilink(&gmwikilink.Node{Fragment: []byte("Intro")})
	if err != nil {
		t.Fatalf("ResolveWikilink() error = %v", err)
	}
	if string(got) != "#embed-2-intro" {
		t.Fatalf("ResolveWikilink() = %q, want %q", string(got), "#embed-2-intro")
	}
	gotOutLinks = resolver.OutLinks()
	if gotOutLinks[1].ResolvedRelPath != host.RelPath {
		t.Fatalf("resolver.OutLinks()[1].ResolvedRelPath = %q, want embedded heading host %q", gotOutLinks[1].ResolvedRelPath, host.RelPath)
	}
	for i := range source.OutLinks {
		if source.OutLinks[i].ResolvedRelPath != "" {
			t.Fatalf("source.OutLinks[%d].ResolvedRelPath = %q, want source note to remain unchanged", i, source.OutLinks[i].ResolvedRelPath)
		}
	}
}

func TestLookupRouteTargetCanonicalizesEncodedAndUnicodeRoutes(t *testing.T) {
	t.Parallel()

	current := &model.Note{RelPath: "index.md", VersionID: "v1"}
	note := &model.Note{RelPath: "cafe.md", Route: "/café/", VersionID: "v1"}
	section := &model.Section{
		RelPath:    "tags",
		SourcePath: "tags/_index.md",
		Route:      "/tags/Cafe\u0301/",
		VersionID:  "v1",
	}
	idx := &model.VaultIndex{
		Notes:           map[string]*model.Note{note.RelPath: note},
		SectionsByRoute: map[string]*model.Section{section.Route: section},
	}

	tests := []struct {
		name        string
		target      string
		wantNote    *model.Note
		wantSection *model.Section
	}{
		{
			name:     "percent-encoded route",
			target:   "/%63af%C3%A9/",
			wantNote: note,
		},
		{
			name:        "unicode-equivalent section route",
			target:      "/tags/Caf%C3%A9/",
			wantSection: section,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := LookupRouteTarget(idx, current, tt.target, "")
			if lookup.Note != tt.wantNote {
				t.Fatalf("LookupRouteTarget(%q).Note = %p, want %p", tt.target, lookup.Note, tt.wantNote)
			}
			if lookup.Section != tt.wantSection {
				t.Fatalf("LookupRouteTarget(%q).Section = %p, want %p", tt.target, lookup.Section, tt.wantSection)
			}
		})
	}
}

type noteOption func(*model.Note)

func testNote(relPath string, slug string, options ...noteOption) *model.Note {
	note := &model.Note{RelPath: relPath, Slug: slug}
	for _, option := range options {
		option(note)
	}
	return note
}

func withAliases(aliases ...string) noteOption {
	return func(note *model.Note) {
		note.Aliases = append([]string(nil), aliases...)
	}
}

func withHeadings(headings ...model.Heading) noteOption {
	return func(note *model.Note) {
		note.Headings = append([]model.Heading(nil), headings...)
	}
}

func buildIndex(public []*model.Note, unpublished []*model.Note) *model.VaultIndex {
	idx := &model.VaultIndex{
		Notes:       make(map[string]*model.Note, len(public)),
		NoteBySlug:  make(map[string]*model.Note, len(public)),
		NoteByName:  make(map[string][]*model.Note),
		AliasByName: make(map[string][]*model.Note),
		Unpublished: model.UnpublishedLookup{
			Notes:       make(map[string]*model.Note, len(unpublished)),
			NoteByName:  make(map[string][]*model.Note),
			AliasByName: make(map[string][]*model.Note),
		},
	}

	for _, note := range public {
		idx.Notes[note.RelPath] = note
		idx.NoteBySlug[note.Slug] = note
		idx.NoteByName[noteLookupKey(note.RelPath)] = append(idx.NoteByName[noteLookupKey(note.RelPath)], note)
		for _, alias := range note.Aliases {
			idx.AliasByName[aliasLookupKey(alias)] = append(idx.AliasByName[aliasLookupKey(alias)], note)
		}
	}

	for _, note := range unpublished {
		idx.Unpublished.Notes[note.RelPath] = note
		idx.Unpublished.NoteByName[noteLookupKey(note.RelPath)] = append(idx.Unpublished.NoteByName[noteLookupKey(note.RelPath)], note)
		for _, alias := range note.Aliases {
			idx.Unpublished.AliasByName[aliasLookupKey(alias)] = append(idx.Unpublished.AliasByName[aliasLookupKey(alias)], note)
		}
	}

	return idx
}
