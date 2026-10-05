package markdown

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/diag"
	"github.com/simp-lee/oxpio/internal/model"
)

func TestNewMarkdownCollectsStandardLinksWithEmptyFragments(t *testing.T) {
	tests := []struct {
		name         string
		destination  string
		ledgerTarget string
	}{
		{name: "without query", destination: "child.md#", ledgerTarget: "child.md#"},
		{name: "with query", destination: "child.md?q=1#", ledgerTarget: "child.md?q=1#"},
		{name: "whitespace fragment", destination: "<child.md# Section>", ledgerTarget: "child.md# Section"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host := &model.Note{
				RelPath:    "notes/host.md",
				Slug:       "notes/host",
				Route:      "/host/",
				RawContent: []byte("[Child](" + tt.destination + ")\n"),
				OutLinks:   []model.LinkRef{{RawTarget: tt.ledgerTarget, Standard: true, Line: 1}},
			}
			child := &model.Note{RelPath: "notes/child.md", Slug: "notes/child", Route: "/child/", Headings: []model.Heading{{Text: "Section", ID: "section"}}}
			index := &model.VaultIndex{
				Notes:       map[string]*model.Note{host.RelPath: host, child.RelPath: child},
				NoteBySlug:  map[string]*model.Note{host.Slug: host, child.Slug: child},
				NoteByName:  map[string][]*model.Note{"host": {host}, "child": {child}},
				AliasByName: map[string][]*model.Note{},
			}
			md, result := NewMarkdown(index, host, nil, diag.NewCollector())
			if err := md.Convert(host.RawContent, &bytes.Buffer{}); err != nil {
				t.Fatalf("Convert() error = %v", err)
			}
			if got := result.OutLinks()[0].ResolvedRelPath; got != child.RelPath {
				t.Fatalf("ResolvedRelPath = %q, want %q", got, child.RelPath)
			}
			if host.OutLinks[0].ResolvedRelPath != "" {
				t.Fatal("rendering mutated the indexed source note")
			}
		})
	}
}

func TestNewMarkdownCollectsSectionSourceLinksWithoutTreatingSectionsAsNotes(t *testing.T) {
	host := &model.Note{
		RelPath:    "host.md",
		Route:      "/host/",
		RawContent: []byte("[Markdown](guide/_index.md#Install)\n\n[[guide/_index#Install|Wikilink]]\n"),
		OutLinks: []model.LinkRef{
			{RawTarget: "guide/_index.md#Install", Standard: true, Line: 1},
			{RawTarget: "guide/_index#Install", Line: 3},
		},
	}
	section := &model.Section{
		RelPath: "guide", SourcePath: "guide/_index.md", Route: "/guide/",
		Headings: []model.Heading{{Level: 2, Text: "Install", ID: "install"}},
	}
	index := &model.VaultIndex{
		Notes:            map[string]*model.Note{host.RelPath: host},
		Sections:         map[string]*model.Section{section.RelPath: section},
		SectionsBySource: map[string]*model.Section{section.SourcePath: section},
		SectionsByRoute:  map[string]*model.Section{section.Route: section},
	}
	collector := diag.NewCollector()
	md, result := NewMarkdown(index, host, nil, collector)

	var output bytes.Buffer
	if err := md.Convert(host.RawContent, &output); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if got := strings.Count(output.String(), `<a href="../guide/#install">`); got != 2 {
		t.Fatalf("rendered section link count = %d, want 2: %s", got, output.String())
	}
	links := result.OutLinks()
	if len(links) != 2 || links[0].ResolvedRelPath != section.SourcePath || links[1].ResolvedRelPath != section.SourcePath {
		t.Fatalf("resolved section-source links = %#v, want source path %q", links, section.SourcePath)
	}
	if index.Notes[section.SourcePath] != nil {
		t.Fatalf("section source %q was represented as an ordinary note", section.SourcePath)
	}
	if got := collector.Diagnostics(); len(got) != 0 {
		t.Fatalf("collector.Diagnostics() = %#v, want no diagnostics", got)
	}
}

func TestNewMarkdownCollectsResolvedStandardLinksIncludingEmbeds(t *testing.T) {
	host := &model.Note{
		RelPath:    "notes/host.md",
		Slug:       "notes/host",
		Route:      "/host/",
		RawContent: []byte("[Child](child.md)\n\n![[Embedded]]\n"),
		OutLinks: []model.LinkRef{{
			RawTarget: "child.md",
			Display:   "Child",
			Standard:  true,
			Line:      1,
		}},
		Embeds: []model.EmbedRef{{Target: "Embedded", Line: 3}},
	}
	embedded := &model.Note{
		RelPath:    "notes/embedded.md",
		Slug:       "notes/embedded",
		Route:      "/embedded/",
		RawContent: []byte("[Child](child.md)\n"),
		OutLinks: []model.LinkRef{{
			RawTarget: "child.md",
			Display:   "Child",
			Standard:  true,
			Line:      1,
		}},
	}
	child := &model.Note{RelPath: "notes/child.md", Slug: "notes/child", Route: "/child/"}
	index := &model.VaultIndex{
		Notes: map[string]*model.Note{
			host.RelPath:     host,
			embedded.RelPath: embedded,
			child.RelPath:    child,
		},
		NoteBySlug: map[string]*model.Note{
			host.Slug:     host,
			embedded.Slug: embedded,
			child.Slug:    child,
		},
		NoteByName: map[string][]*model.Note{
			"host":     {host},
			"embedded": {embedded},
			"child":    {child},
		},
		AliasByName: map[string][]*model.Note{},
	}
	collector := diag.NewCollector()
	md, result := NewMarkdown(index, host, nil, collector)

	var output bytes.Buffer
	if err := md.Convert(host.RawContent, &output); err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if got := strings.Count(output.String(), `<a href="../child/">Child</a>`); got != 2 {
		t.Fatalf("rendered child link count = %d, want 2: %s", got, output.String())
	}

	links := result.OutLinks()
	gotTargets := make([]string, 0, len(links))
	for _, ref := range links {
		gotTargets = append(gotTargets, ref.ResolvedRelPath)
	}
	wantTargets := []string{child.RelPath, child.RelPath, embedded.RelPath}
	if !reflect.DeepEqual(gotTargets, wantTargets) {
		t.Fatalf("resolved render-local targets = %#v, want %#v", gotTargets, wantTargets)
	}
	if host.OutLinks[0].ResolvedRelPath != "" || embedded.OutLinks[0].ResolvedRelPath != "" {
		t.Fatal("rendering mutated an indexed source note's link ledger")
	}
	if got := collector.Diagnostics(); len(got) != 0 {
		t.Fatalf("collector.Diagnostics() = %#v, want no diagnostics", got)
	}
}
