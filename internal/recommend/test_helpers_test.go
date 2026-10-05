package recommend

import "github.com/simp-lee/oxpio/internal/model"

func testRecommendIndex(notes ...*model.Note) *model.VaultIndex {
	idx := &model.VaultIndex{Notes: make(map[string]*model.Note, len(notes))}
	for _, note := range notes {
		if note != nil {
			idx.Notes[note.RelPath] = note
		}
	}
	return idx
}
