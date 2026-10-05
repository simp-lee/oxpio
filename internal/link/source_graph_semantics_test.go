package link

import (
	"reflect"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestBuildSourceGraphPreservesWikilinkLookupSemantics(t *testing.T) {
	tests := []struct {
		name     string
		outLinks []model.LinkRef
		embeds   []model.EmbedRef
		want     []string
	}{
		{name: "wikilink vault path", outLinks: []model.LinkRef{{RawTarget: "docs/target.md"}}, want: []string{"docs/target.md"}},
		{name: "wikilink basename with extension", outLinks: []model.LinkRef{{RawTarget: "target.md"}}, want: []string{"docs/target.md"}},
		{name: "note embed vault path", embeds: []model.EmbedRef{{Target: "docs/target.md"}}, want: []string{"docs/target.md"}},
		{name: "note embed basename with extension", embeds: []model.EmbedRef{{Target: "target.md"}}, want: []string{"docs/target.md"}},
		{name: "standard link stays source relative", outLinks: []model.LinkRef{{RawTarget: "target.md", Standard: true}}, want: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			host := testNote("notes/host.md", "notes/host")
			host.OutLinks = tt.outLinks
			host.Embeds = tt.embeds
			target := testNote("docs/target.md", "docs/target")

			graph := BuildSourceGraph(buildIndex([]*model.Note{host, target}, nil))
			if got := graph.Forward[host.RelPath]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("graph.Forward[%q] = %#v, want %#v", host.RelPath, got, tt.want)
			}
		})
	}
}
