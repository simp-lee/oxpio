package embed

import (
	"bufio"
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/resourcepath"
	gast "github.com/yuin/goldmark/ast"
	gmwikilink "go.abhg.dev/goldmark/wikilink"
)

func TestImageAssetCandidatesIncludeAttachmentFolderFallback(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/current.md"}
	idx := &model.VaultIndex{AttachmentFolderPath: "assets/uploads"}

	got := resourcepath.CandidatePathsWithAttachmentFolder(note, idx.AttachmentFolderPath, "diagram.png")
	want := []string{"notes/diagram.png", "diagram.png", "assets/uploads/diagram.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CandidatePathsWithAttachmentFolder() = %#v, want %#v", got, want)
	}
}

func TestSelectEmbedSourceUsesHeadingSectionRange(t *testing.T) {
	t.Parallel()

	raw := []byte("# Top\n\n## Section\n\nWanted\n\n## Later\n\nSkip\n")
	start := strings.Index(string(raw), "## Section")
	end := strings.Index(string(raw), "## Later")
	note := &model.Note{
		RawContent: raw,
		HeadingSections: map[string]model.SectionRange{
			"section": {StartOffset: start, EndOffset: end},
		},
	}

	got := string(selectEmbedSource(note, "section"))
	want := "## Section\n\nWanted\n\n"
	if got != want {
		t.Fatalf("selectEmbedSource() = %q, want %q", got, want)
	}
}

func TestScopeNoteToSectionEmbedsAdjustsBodyStartLine(t *testing.T) {
	t.Parallel()

	raw := "# Top\n\n## Section\n\nWanted\n"
	start := strings.Index(raw, "## Section")
	note := &model.Note{
		RawContent:    []byte(raw),
		BodyStartLine: 4,
		HeadingSections: map[string]model.SectionRange{
			"section": {StartOffset: start, EndOffset: len(raw)},
		},
	}

	scoped := scopeNoteToSectionEmbeds(note, "section")
	if scoped == nil {
		t.Fatal("scopeNoteToSectionEmbeds() = nil, want scoped note")
	}
	if scoped.BodyStartLine != 6 {
		t.Fatalf("scopeNoteToSectionEmbeds().BodyStartLine = %d, want %d", scoped.BodyStartLine, 6)
	}
	if note.BodyStartLine != 4 {
		t.Fatalf("note.BodyStartLine = %d, want source note unchanged", note.BodyStartLine)
	}
}

func TestScopeNoteToSectionEmbedsRebasesNestedSectionOffsets(t *testing.T) {
	t.Parallel()

	raw := "# Top\n\n## Outer\nOuter text\n\n### Inner\nInner text\n\n[[Target]]\n![[Nested]]\n"
	outerStart := strings.Index(raw, "## Outer")
	innerStart := strings.Index(raw, "### Inner")
	outerSection := model.SectionRange{StartOffset: outerStart, EndOffset: len(raw)}
	note := &model.Note{
		RawContent:    []byte(raw),
		BodyStartLine: 10,
		Headings: []model.Heading{
			{Level: 2, Text: "Outer", ID: "outer"},
			{Level: 3, Text: "Inner", ID: "inner"},
		},
		HeadingSections: map[string]model.SectionRange{
			"outer": outerSection,
			"inner": {StartOffset: innerStart, EndOffset: len(raw)},
		},
		OutLinks: []model.LinkRef{{
			RawTarget: "Target",
			Line:      18,
			Offset:    strings.Index(raw, "[[Target]]"),
		}},
		Embeds: []model.EmbedRef{{
			Target: "Nested",
			Line:   19,
			Offset: strings.Index(raw, "![[Nested]]"),
		}},
	}

	outer := scopeNoteToSectionEmbeds(note, "outer")
	if outer == nil {
		t.Fatal("scopeNoteToSectionEmbeds(outer) = nil, want scoped note")
	}
	if outer.BodyStartLine != 12 {
		t.Fatalf("outer.BodyStartLine = %d, want %d", outer.BodyStartLine, 12)
	}
	if got, want := string(outer.RawContent), raw[outerStart:]; got != want {
		t.Fatalf("outer.RawContent = %q, want %q", got, want)
	}
	if got, want := outer.HeadingSections["outer"], (model.SectionRange{StartOffset: 0, EndOffset: len(raw) - outerStart}); !reflect.DeepEqual(got, want) {
		t.Fatalf("outer.HeadingSections[outer] = %#v, want %#v", got, want)
	}
	if got, want := outer.HeadingSections["inner"], (model.SectionRange{StartOffset: innerStart - outerStart, EndOffset: len(raw) - outerStart}); !reflect.DeepEqual(got, want) {
		t.Fatalf("outer.HeadingSections[inner] = %#v, want %#v", got, want)
	}
	if got, want := outer.OutLinks[0].Offset, strings.Index(string(outer.RawContent), "[[Target]]"); got != want {
		t.Fatalf("outer.OutLinks[0].Offset = %d, want %d", got, want)
	}
	if got, want := outer.Embeds[0].Offset, strings.Index(string(outer.RawContent), "![[Nested]]"); got != want {
		t.Fatalf("outer.Embeds[0].Offset = %d, want %d", got, want)
	}

	inner := scopeNoteToSectionEmbeds(outer, "inner")
	if inner == nil {
		t.Fatal("scopeNoteToSectionEmbeds(inner) = nil, want scoped note")
	}
	if inner.BodyStartLine != 15 {
		t.Fatalf("inner.BodyStartLine = %d, want %d", inner.BodyStartLine, 15)
	}
	if got, want := string(inner.RawContent), raw[innerStart:]; got != want {
		t.Fatalf("inner.RawContent = %q, want %q", got, want)
	}
	if got, want := inner.HeadingSections["inner"], (model.SectionRange{StartOffset: 0, EndOffset: len(raw) - innerStart}); !reflect.DeepEqual(got, want) {
		t.Fatalf("inner.HeadingSections[inner] = %#v, want %#v", got, want)
	}
	if got, want := inner.OutLinks[0].Offset, strings.Index(string(inner.RawContent), "[[Target]]"); got != want {
		t.Fatalf("inner.OutLinks[0].Offset = %d, want %d", got, want)
	}
	if got, want := inner.Embeds[0].Offset, strings.Index(string(inner.RawContent), "![[Nested]]"); got != want {
		t.Fatalf("inner.Embeds[0].Offset = %d, want %d", got, want)
	}
	if inner.OutLinks[0].Line != 18 {
		t.Fatalf("inner.OutLinks[0].Line = %d, want %d", inner.OutLinks[0].Line, 18)
	}
	if inner.Embeds[0].Line != 19 {
		t.Fatalf("inner.Embeds[0].Line = %d, want %d", inner.Embeds[0].Line, 19)
	}
	if note.OutLinks[0].Offset != strings.Index(raw, "[[Target]]") {
		t.Fatalf("note.OutLinks[0].Offset = %d, want source note unchanged", note.OutLinks[0].Offset)
	}
	if note.Embeds[0].Offset != strings.Index(raw, "![[Nested]]") {
		t.Fatalf("note.Embeds[0].Offset = %d, want source note unchanged", note.Embeds[0].Offset)
	}
}

func TestEmbedAltTextFallsBackToFilenameStem(t *testing.T) {
	t.Parallel()

	if got := embedAltText("600", "diagram.png", "assets/uploads/diagram.png"); got != "diagram" {
		t.Fatalf("embedAltText() = %q, want %q", got, "diagram")
	}
	if got := embedAltText("Shown Label", "diagram.png", "assets/uploads/diagram.png"); got != "Shown Label" {
		t.Fatalf("embedAltText() = %q, want %q", got, "Shown Label")
	}
}

func TestRenderEmbedFallsBackToPlainTextAndLinkForBlockReferences(t *testing.T) {
	t.Parallel()

	current := &model.Note{
		Slug:    "current",
		RelPath: "notes/current.md",
		Embeds:  []model.EmbedRef{{Target: "Guide", Fragment: "^block-ref", Line: 3}},
	}
	target := &model.Note{Slug: "guide", RelPath: "notes/guide.md"}
	idx := &model.VaultIndex{
		Notes: map[string]*model.Note{
			current.RelPath: current,
			target.RelPath:  target,
		},
		NoteBySlug: map[string]*model.Note{
			current.Slug: current,
			target.Slug:  target,
		},
		NoteByName: map[string][]*model.Note{
			"current": {current},
			"guide":   {target},
		},
		AliasByName: map[string][]*model.Note{},
	}
	collector := diag.NewCollector()
	renderer := &wikilinkHTMLRenderer{
		index:       idx,
		currentNote: current,
		outputNote:  current,
		diag:        collector,
	}
	node := &gmwikilink.Node{Target: []byte("Guide"), Fragment: []byte("^block-ref"), Embed: true}

	var raw bytes.Buffer
	buf := bufio.NewWriter(&raw)
	status, err := renderer.renderEmbed(buf, nil, node)
	if err != nil {
		t.Fatalf("renderEmbed() error = %v", err)
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("buf.Flush() error = %v", err)
	}
	if status != gast.WalkSkipChildren {
		t.Fatalf("renderEmbed() status = %v, want %v", status, gast.WalkSkipChildren)
	}

	html := raw.String()
	if !strings.Contains(html, `Guide#^block-ref`) {
		t.Fatalf("HTML = %q, want raw block reference text preserved", html)
	}
	if !strings.Contains(html, `<a href="../guide/">open note</a>`) {
		t.Fatalf("HTML = %q, want fallback link to the resolved note", html)
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     diag.KindUnsupportedSyntax,
		Location: diag.Location{Path: current.RelPath, Line: 3},
		Target:   "Guide#^block-ref",
		Message:  `embed "Guide#^block-ref" block reference embeds are not supported; rendering as plain text with a link`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestRenderEmbedFallsBackToPlainTextForUnpublishedBlockReferences(t *testing.T) {
	t.Parallel()

	current := &model.Note{
		Slug:    "current",
		RelPath: "notes/current.md",
		Embeds:  []model.EmbedRef{{Target: "Private", Fragment: "^block-ref", Line: 7}},
	}
	unpublished := &model.Note{Slug: "private/secret", RelPath: "private/secret.md"}
	idx := &model.VaultIndex{
		Notes: map[string]*model.Note{
			current.RelPath: current,
		},
		NoteBySlug: map[string]*model.Note{
			current.Slug: current,
		},
		NoteByName: map[string][]*model.Note{
			"current": {current},
		},
		AliasByName: map[string][]*model.Note{},
		Unpublished: model.UnpublishedLookup{
			Notes: map[string]*model.Note{
				unpublished.RelPath: unpublished,
			},
			NoteByName: map[string][]*model.Note{
				"private": {unpublished},
			},
			AliasByName: map[string][]*model.Note{},
		},
	}
	collector := diag.NewCollector()
	renderer := &wikilinkHTMLRenderer{
		index:       idx,
		currentNote: current,
		outputNote:  current,
		diag:        collector,
	}
	node := &gmwikilink.Node{Target: []byte("Private"), Fragment: []byte("^block-ref"), Embed: true}

	var raw bytes.Buffer
	buf := bufio.NewWriter(&raw)
	status, err := renderer.renderEmbed(buf, nil, node)
	if err != nil {
		t.Fatalf("renderEmbed() error = %v", err)
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("buf.Flush() error = %v", err)
	}
	if status != gast.WalkSkipChildren {
		t.Fatalf("renderEmbed() status = %v, want %v", status, gast.WalkSkipChildren)
	}

	html := raw.String()
	if !strings.Contains(html, `Private#^block-ref`) {
		t.Fatalf("HTML = %q, want raw block reference text preserved", html)
	}
	for _, forbidden := range []string{`href=`, `open note`, `private/secret`} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("HTML = %q, want %q omitted for unpublished block-reference fallback", html, forbidden)
		}
	}

	want := []diag.Diagnostic{{
		Severity: diag.SeverityWarning,
		Kind:     diag.KindUnsupportedSyntax,
		Location: diag.Location{Path: current.RelPath, Line: 7},
		Target:   "Private#^block-ref",
		Message:  `embed "Private#^block-ref" block reference embeds are not supported; rendering as plain text`,
	}}
	if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
		t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
	}
}

func TestRenderEmbedFallsBackToVisiblePlainTextForDegradedNoteEmbeds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		embed          model.EmbedRef
		configureIndex func(*model.Note) *model.VaultIndex
		depth          int
		visited        map[string]struct{}
		wantHTML       string
		wantDiag       diag.Diagnostic
	}{
		{
			name:  "dead note",
			embed: model.EmbedRef{Target: "Missing", Line: 4},
			configureIndex: func(current *model.Note) *model.VaultIndex {
				return &model.VaultIndex{
					Notes: map[string]*model.Note{
						current.RelPath: current,
					},
					NoteBySlug: map[string]*model.Note{
						current.Slug: current,
					},
					NoteByName: map[string][]*model.Note{
						"current": {current},
					},
					AliasByName: map[string][]*model.Note{},
				}
			},
			wantHTML: "Missing",
			wantDiag: diag.Diagnostic{
				Severity: diag.SeverityWarning,
				Kind:     diag.KindDeadLink,
				Location: diag.Location{Path: "notes/current.md", Line: 4},
				Target:   "Missing",
				Message:  `note embed "Missing" could not be resolved; rendering as plain text`,
			},
		},
		{
			name:  "canvas resource",
			embed: model.EmbedRef{Target: "board.canvas", Line: 5},
			configureIndex: func(current *model.Note) *model.VaultIndex {
				idx := &model.VaultIndex{
					Notes: map[string]*model.Note{
						current.RelPath: current,
					},
					NoteBySlug: map[string]*model.Note{
						current.Slug: current,
					},
					NoteByName: map[string][]*model.Note{
						"current": {current},
					},
					AliasByName: map[string][]*model.Note{},
				}
				idx.SetResources([]string{"board.canvas"})
				return idx
			},
			wantHTML: "board.canvas",
			wantDiag: diag.Diagnostic{
				Severity: diag.SeverityWarning,
				Kind:     diag.KindUnsupportedSyntax,
				Location: diag.Location{Path: "notes/current.md", Line: 5},
				Target:   "board.canvas",
				Message:  `embed "board.canvas" targets unsupported canvas content; rendering as plain text`,
			},
		},
		{
			name:  "ambiguous canvas basename fallback",
			embed: model.EmbedRef{Target: "plan.canvas", Line: 6},
			configureIndex: func(current *model.Note) *model.VaultIndex {
				idx := &model.VaultIndex{
					Notes: map[string]*model.Note{
						current.RelPath: current,
					},
					NoteBySlug: map[string]*model.Note{
						current.Slug: current,
					},
					NoteByName: map[string][]*model.Note{
						"current": {current},
					},
					AliasByName: map[string][]*model.Note{},
				}
				idx.SetResources([]string{"boards/plan.canvas", "archive/plan.canvas"})
				return idx
			},
			wantHTML: "plan.canvas",
			wantDiag: diag.Diagnostic{
				Severity: diag.SeverityWarning,
				Kind:     diag.KindUnsupportedSyntax,
				Location: diag.Location{Path: "notes/current.md", Line: 6},
				Target:   "plan.canvas",
				Message:  `embed "plan.canvas" matched multiple canvas resources after canonical lookup (archive/plan.canvas, boards/plan.canvas); refusing canonical fallback and rendering as plain text`,
			},
		},
		{
			name:  "unpublished note",
			embed: model.EmbedRef{Target: "Private", Line: 6},
			configureIndex: func(current *model.Note) *model.VaultIndex {
				unpublished := &model.Note{Slug: "private/secret", RelPath: "private/secret.md"}
				return &model.VaultIndex{
					Notes: map[string]*model.Note{
						current.RelPath: current,
					},
					NoteBySlug: map[string]*model.Note{
						current.Slug: current,
					},
					NoteByName: map[string][]*model.Note{
						"current": {current},
					},
					AliasByName: map[string][]*model.Note{},
					Unpublished: model.UnpublishedLookup{
						Notes: map[string]*model.Note{
							unpublished.RelPath: unpublished,
						},
						NoteByName: map[string][]*model.Note{
							"private": {unpublished},
						},
						AliasByName: map[string][]*model.Note{},
					},
				}
			},
			wantHTML: "Private",
			wantDiag: diag.Diagnostic{
				Severity: diag.SeverityWarning,
				Kind:     kindUnpublishedEmbed,
				Location: diag.Location{Path: "notes/current.md", Line: 6},
				Target:   "Private",
				Message:  `note embed "Private" points to unpublished note "private/secret.md"; rendering as plain text`,
			},
		},
		{
			name:  "missing fragment",
			embed: model.EmbedRef{Target: "Guide", Fragment: "Missing Heading", Line: 7},
			configureIndex: func(current *model.Note) *model.VaultIndex {
				target := &model.Note{Slug: "guide", RelPath: "notes/guide.md", RawContent: []byte("# Guide\n\nBody.\n")}
				return &model.VaultIndex{
					Notes: map[string]*model.Note{
						current.RelPath: current,
						target.RelPath:  target,
					},
					NoteBySlug: map[string]*model.Note{
						current.Slug: current,
						target.Slug:  target,
					},
					NoteByName: map[string][]*model.Note{
						"current": {current},
						"guide":   {target},
					},
					AliasByName: map[string][]*model.Note{},
				}
			},
			wantHTML: "Guide#Missing Heading",
			wantDiag: diag.Diagnostic{
				Severity: diag.SeverityWarning,
				Kind:     diag.KindDeadLink,
				Location: diag.Location{Path: "notes/current.md", Line: 7},
				Target:   "Guide#Missing Heading",
				Message:  `note embed "Guide#Missing Heading" points to missing heading "Missing Heading" in "notes/guide.md"; rendering as plain text`,
			},
		},
		{
			name:  "max depth",
			embed: model.EmbedRef{Target: "Guide", Line: 8},
			configureIndex: func(current *model.Note) *model.VaultIndex {
				target := &model.Note{Slug: "guide", RelPath: "notes/guide.md", RawContent: []byte("# Guide\n\nBody.\n")}
				return &model.VaultIndex{
					Notes: map[string]*model.Note{
						current.RelPath: current,
						target.RelPath:  target,
					},
					NoteBySlug: map[string]*model.Note{
						current.Slug: current,
						target.Slug:  target,
					},
					NoteByName: map[string][]*model.Note{
						"current": {current},
						"guide":   {target},
					},
					AliasByName: map[string][]*model.Note{},
				}
			},
			depth:    maxDepth,
			wantHTML: "Guide",
			wantDiag: diag.Diagnostic{
				Severity: diag.SeverityWarning,
				Kind:     diag.KindUnsupportedSyntax,
				Location: diag.Location{Path: "notes/current.md", Line: 8},
				Target:   "Guide",
				Message:  `embed "Guide" maximum embed depth of 10 exceeded; rendering as plain text`,
			},
		},
		{
			name:  "cycle",
			embed: model.EmbedRef{Target: "Guide", Line: 9},
			configureIndex: func(current *model.Note) *model.VaultIndex {
				target := &model.Note{Slug: "guide", RelPath: "notes/guide.md", RawContent: []byte("# Guide\n\nBody.\n")}
				return &model.VaultIndex{
					Notes: map[string]*model.Note{
						current.RelPath: current,
						target.RelPath:  target,
					},
					NoteBySlug: map[string]*model.Note{
						current.Slug: current,
						target.Slug:  target,
					},
					NoteByName: map[string][]*model.Note{
						"current": {current},
						"guide":   {target},
					},
					AliasByName: map[string][]*model.Note{},
				}
			},
			visited:  map[string]struct{}{"notes/guide.md": {}},
			wantHTML: "Guide",
			wantDiag: diag.Diagnostic{
				Severity: diag.SeverityWarning,
				Kind:     diag.KindUnsupportedSyntax,
				Location: diag.Location{Path: "notes/current.md", Line: 9},
				Target:   "Guide",
				Message:  `note embed "Guide" would create a transclusion cycle (notes/guide.md -> notes/guide.md); rendering as plain text`,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			current := &model.Note{
				Slug:    "current",
				RelPath: "notes/current.md",
				Embeds:  []model.EmbedRef{tt.embed},
			}
			idx := tt.configureIndex(current)
			collector := diag.NewCollector()
			renderer := &wikilinkHTMLRenderer{
				index:       idx,
				currentNote: current,
				outputNote:  current,
				diag:        collector,
				depth:       tt.depth,
				visited:     cloneVisited(tt.visited),
			}
			node := &gmwikilink.Node{Target: []byte(tt.embed.Target), Fragment: []byte(tt.embed.Fragment), Embed: true}

			var raw bytes.Buffer
			buf := bufio.NewWriter(&raw)
			status, err := renderer.renderEmbed(buf, nil, node)
			if err != nil {
				t.Fatalf("renderEmbed() error = %v", err)
			}
			if err := buf.Flush(); err != nil {
				t.Fatalf("buf.Flush() error = %v", err)
			}
			if status != gast.WalkSkipChildren {
				t.Fatalf("renderEmbed() status = %v, want %v", status, gast.WalkSkipChildren)
			}

			html := raw.String()
			if !strings.Contains(html, tt.wantHTML) {
				t.Fatalf("HTML = %q, want visible fallback %q", html, tt.wantHTML)
			}

			want := []diag.Diagnostic{tt.wantDiag}
			if got := collector.Diagnostics(); !reflect.DeepEqual(got, want) {
				t.Fatalf("collector.Diagnostics() = %#v, want %#v", got, want)
			}
		})
	}
}
