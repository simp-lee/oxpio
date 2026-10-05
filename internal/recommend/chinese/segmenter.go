// Copyright 2013 Hui Chen
// Copyright 2016 ego authors
// Copyright 2016 The go-ego Project Developers.
//
// Modified by the OXPIO authors in 2026 to retain only immutable embedded
// Chinese dictionary DAG/HMM segmentation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package chinese

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/vcaesar/cedar"
)

type dictionaryToken struct {
	frequency float64
}

type dictionary struct {
	trie           *cedar.Cedar
	tokens         []dictionaryToken
	totalFrequency float64
}

type route struct {
	weight float64
	end    int
}

// Segmenter is an immutable Chinese DAG/HMM segmenter after construction.
type Segmenter struct {
	dictionary *dictionary
	hmm        *hmmModel
	stopwords  map[string]struct{}
}

type initializer struct {
	once    sync.Once
	load    func() (*Segmenter, error)
	value   *Segmenter
	loadErr error
}

var defaultInitializer = initializer{load: loadEmbeddedSegmenter}

// Default returns the lazily initialized segmenter backed by embedded fixed resources.
func Default() (*Segmenter, error) {
	return defaultInitializer.get()
}

// Cut initializes the default segmenter and applies accurate DAG/HMM cutting.
func Cut(text string) ([]string, error) {
	segmenter, err := Default()
	if err != nil {
		return nil, err
	}
	return segmenter.Cut(text, true), nil
}

func (i *initializer) get() (*Segmenter, error) {
	i.once.Do(func() {
		i.value, i.loadErr = i.load()
	})
	return i.value, i.loadErr
}

func loadEmbeddedSegmenter() (*Segmenter, error) {
	return newSegmenter(simplifiedDictionary, traditionalDictionary, chineseStopwords)
}

func newSegmenter(simplified string, traditional string, stopwords string) (*Segmenter, error) {
	dict := &dictionary{trie: cedar.New()}
	if err := dict.load(simplified); err != nil {
		return nil, err
	}
	if err := dict.load(traditional); err != nil {
		return nil, err
	}
	if dict.totalFrequency <= 0 {
		return nil, errors.New("chinese dictionary has no usable entries")
	}

	return &Segmenter{
		dictionary: dict,
		hmm:        newHMMModel(),
		stopwords:  loadStopwords(stopwords),
	}, nil
}

func (d *dictionary) load(data string) error {
	for line := range strings.SplitSeq(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return errors.New("chinese dictionary entry is missing frequency")
		}

		frequency, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return err
		}
		if frequency < 2 {
			continue
		}
		if len([]rune(fields[0])) < 2 {
			frequency = 2
		}
		if err := d.add(fields[0], frequency); err != nil {
			return err
		}
	}
	return nil
}

func (d *dictionary) add(word string, frequency float64) error {
	key := []byte(word)
	if _, err := d.trie.Get(key); err == nil {
		return nil
	}

	value := len(d.tokens)
	if err := d.trie.Insert(key, value); err != nil {
		return err
	}
	d.tokens = append(d.tokens, dictionaryToken{frequency: frequency})
	d.totalFrequency += frequency
	return nil
}

// lookup reports the exact frequency and whether word remains a dictionary prefix.
func (d *dictionary) lookup(word string) (frequency float64, prefix bool) {
	if d == nil || d.trie == nil || word == "" {
		return 0, false
	}

	id, err := d.trie.Jump([]byte(word), 0)
	if err != nil {
		return 0, false
	}
	value, err := d.trie.Value(id)
	if err != nil {
		return 0, true
	}
	if value < 0 || value >= len(d.tokens) {
		return 0, true
	}
	return d.tokens[value].frequency, true
}

// Cut segments one Han span in exact DAG mode, optionally applying HMM to
// consecutive unknown single-rune routes. It does not emit search-mode subwords.
func (s *Segmenter) Cut(text string, useHMM bool) []string {
	if s == nil || s.dictionary == nil || text == "" {
		return nil
	}

	runes := []rune(text)
	routes := s.calculateRoutes(runes)
	result := make([]string, 0, len(runes))
	unknown := make([]rune, 0, 8)

	flushUnknown := func() {
		if len(unknown) == 0 {
			return
		}
		if !useHMM || len(unknown) == 1 {
			for _, current := range unknown {
				result = append(result, string(current))
			}
			unknown = unknown[:0]
			return
		}

		word := string(unknown)
		frequency, _ := s.dictionary.lookup(word)
		if frequency > 0 {
			for _, current := range unknown {
				result = append(result, string(current))
			}
		} else {
			result = append(result, s.hmm.cut(unknown)...)
		}
		unknown = unknown[:0]
	}

	for start := 0; start < len(runes); {
		end := routes[start].end + 1
		if end-start == 1 {
			unknown = append(unknown, runes[start])
		} else {
			flushUnknown()
			result = append(result, string(runes[start:end]))
		}
		start = end
	}
	flushUnknown()
	return result
}

// IsStopword reports membership in the fixed embedded Chinese stopword set.
func (s *Segmenter) IsStopword(token string) bool {
	if s == nil || token == "" {
		return false
	}
	_, ok := s.stopwords[token]
	return ok
}

func (s *Segmenter) calculateRoutes(runes []rune) []route {
	routes := make([]route, len(runes)+1)
	logTotal := math.Log(s.dictionary.totalFrequency)
	for start := len(runes) - 1; start >= 0; start-- {
		best := route{weight: math.Inf(-1), end: start}
		found := false
		for end := start; end < len(runes); end++ {
			frequency, prefix := s.dictionary.lookup(string(runes[start : end+1]))
			if !prefix {
				break
			}
			if frequency <= 0 {
				continue
			}

			candidate := math.Log(frequency) - logTotal + routes[end+1].weight
			if !found || candidate > best.weight || (candidate == best.weight && end > best.end) {
				best = route{weight: candidate, end: end}
				found = true
			}
		}
		if !found {
			best = route{weight: -logTotal + routes[start+1].weight, end: start}
		}
		routes[start] = best
	}
	return routes
}

func loadStopwords(data string) map[string]struct{} {
	words := make(map[string]struct{})
	for line := range strings.SplitSeq(data, "\n") {
		word := strings.TrimSpace(line)
		if word != "" {
			words[word] = struct{}{}
		}
	}
	return words
}
