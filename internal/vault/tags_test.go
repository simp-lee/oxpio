package vault

import (
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func TestBuildTagIndexDoesNotInventParentTags(t *testing.T) {
	note := &model.Note{RelPath: "article.md", Tags: []string{"parent/child"}}
	index := buildTagIndex([]*model.Note{note})
	if index["parent/child"] == nil {
		t.Fatal("missing explicitly used hierarchical tag")
	}
	if _, ok := index["parent"]; ok {
		t.Fatal("generated an unused parent tag page")
	}
}
