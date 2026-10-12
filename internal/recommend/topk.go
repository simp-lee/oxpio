package recommend

import "container/heap"

// RankedDocument is one compact candidate retained by the bounded engine.
type RankedDocument struct {
	DocID   int
	Score   float64
	Signals PairScore
}

type rankingDocument struct {
	normalizedPath string
	displayTitle   string
}

type topKHeap struct {
	items     []RankedDocument
	documents []rankingDocument
}

func newTopKHeap(capacity int, documents []rankingDocument) *topKHeap {
	return &topKHeap{
		items:     make([]RankedDocument, 0, capacity),
		documents: documents,
	}
}

func (h topKHeap) Len() int { return len(h.items) }

func (h topKHeap) Less(left int, right int) bool {
	return rankedDocumentIsBetter(h.items[right], h.items[left], h.documents)
}

func (h topKHeap) Swap(left int, right int) {
	h.items[left], h.items[right] = h.items[right], h.items[left]
}

func (h *topKHeap) Push(value any) {
	h.items = append(h.items, value.(RankedDocument))
}

func (h *topKHeap) Pop() any {
	last := len(h.items) - 1
	value := h.items[last]
	h.items = h.items[:last]
	return value
}

func (h *topKHeap) add(candidate RankedDocument, capacity int) {
	if capacity <= 0 {
		return
	}
	if len(h.items) < capacity {
		heap.Push(h, candidate)
		return
	}
	if rankedDocumentIsBetter(candidate, h.items[0], h.documents) {
		h.items[0] = candidate
		heap.Fix(h, 0)
	}
}

func rankedDocumentIsBetter(left RankedDocument, right RankedDocument, documents []rankingDocument) bool {
	if left.Score != right.Score {
		return left.Score > right.Score
	}
	leftDocument := documents[left.DocID]
	rightDocument := documents[right.DocID]
	if leftDocument.normalizedPath != rightDocument.normalizedPath {
		return leftDocument.normalizedPath < rightDocument.normalizedPath
	}
	if leftDocument.displayTitle != rightDocument.displayTitle {
		return leftDocument.displayTitle < rightDocument.displayTitle
	}
	return left.DocID < right.DocID
}
