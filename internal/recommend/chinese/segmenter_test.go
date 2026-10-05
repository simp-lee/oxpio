package chinese

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSegmenterGoldens(t *testing.T) {
	segmenter, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}

	tests := []struct {
		text string
		want []string
	}{
		{text: "分布式数据库一致性协议", want: []string{"分布式", "数据库", "一致性", "协议"}},
		{text: "臺灣繁體中文資料庫設計", want: []string{"臺灣", "繁體中文", "資料庫", "設計"}},
		{text: "杭研大厦", want: []string{"杭研", "大厦"}},
	}
	for _, test := range tests {
		if got := segmenter.Cut(test.text, true); !reflect.DeepEqual(got, test.want) {
			t.Errorf("Cut(%q, HMM) = %#v, want %#v", test.text, got, test.want)
		}
	}
}

func TestHMMUnknownWord(t *testing.T) {
	segmenter, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}

	if got, want := segmenter.Cut("杭研大厦", false), []string{"杭", "研", "大厦"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Cut(杭研大厦, no HMM) = %#v, want %#v", got, want)
	}
	if got, want := segmenter.Cut("杭研大厦", true), []string{"杭研", "大厦"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Cut(杭研大厦, HMM) = %#v, want %#v", got, want)
	}

	const outOfModel = "㐀㐁㐂㐃"
	got := segmenter.Cut(outOfModel, true)
	if strings.Join(got, "") != outOfModel {
		t.Fatalf("Cut(out-of-model Han, HMM) = %#v, want lossless output for %q", got, outOfModel)
	}
	if repeated := segmenter.Cut(outOfModel, true); !reflect.DeepEqual(repeated, got) {
		t.Fatalf("repeated Cut(out-of-model Han, HMM) = %#v, want %#v", repeated, got)
	}
}

func TestLazyInitialization(t *testing.T) {
	var calls atomic.Int32
	lazy := initializer{load: func() (*Segmenter, error) {
		calls.Add(1)
		return &Segmenter{}, nil
	}}
	if calls.Load() != 0 {
		t.Fatalf("loader calls before get = %d, want 0", calls.Load())
	}

	first, err := lazy.get()
	if err != nil {
		t.Fatalf("first get error = %v", err)
	}
	second, err := lazy.get()
	if err != nil {
		t.Fatalf("second get error = %v", err)
	}
	if first != second {
		t.Fatal("lazy initializer returned different segmenters")
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1", calls.Load())
	}
}

func TestInitializationError(t *testing.T) {
	want := errors.New("dictionary failed")
	var calls atomic.Int32
	lazy := initializer{load: func() (*Segmenter, error) {
		calls.Add(1)
		return nil, want
	}}

	for attempt := 0; attempt < 2; attempt++ {
		gotSegmenter, gotErr := lazy.get()
		if gotSegmenter != nil {
			t.Fatalf("get attempt %d segmenter = %#v, want nil", attempt, gotSegmenter)
		}
		if gotErr != want {
			t.Fatalf("get attempt %d error = %v, want original error %v", attempt, gotErr, want)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d, want 1 after cached failure", calls.Load())
	}
}

func TestConcurrentCut(t *testing.T) {
	segmenter, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	want := segmenter.Cut("分布式数据库一致性协议杭研大厦", true)

	const workers = 32
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 20; iteration++ {
				if got := segmenter.Cut("分布式数据库一致性协议杭研大厦", true); !reflect.DeepEqual(got, want) {
					errorsByWorker <- fmt.Errorf("Cut() = %#v, want %#v", got, want)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Error(err)
	}
}

func TestEmbeddedResourceHashes(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "simplified", data: simplifiedDictionary, want: "2b3063ec552327520bee3c0c5819d6e131ab3db50a60b94641ec90f611c24bcd"},
		{name: "traditional", data: traditionalDictionary, want: "2c84cef353d2daac62cc62bbeabab6b6a8866cfee8f9f88901e00ed66ed208c6"},
		{name: "stopwords", data: chineseStopwords, want: "8a05af1a224e40d06fce2081ad4d4b2c5e5c902f0a7501c0dba677ce1ee40c90"},
	}
	for _, test := range tests {
		got := fmt.Sprintf("%x", sha256.Sum256([]byte(test.data)))
		if got != test.want {
			t.Errorf("%s resource SHA-256 = %s, want %s", test.name, got, test.want)
		}
	}

	hmmSource, err := os.ReadFile("hmm_model.go")
	if err != nil {
		t.Fatalf("ReadFile(hmm_model.go) error = %v", err)
	}
	const wantHMMSource = "a85802ff04319e4f8043b7ae5c9442ca62747f3a3e6d6d731ab058a3678eed58"
	if got := fmt.Sprintf("%x", sha256.Sum256(hmmSource)); got != wantHMMSource {
		t.Errorf("hmm_model.go SHA-256 = %s, want %s", got, wantHMMSource)
	}
}

func TestThirdPartyInventory(t *testing.T) {
	inventory, err := os.ReadFile("THIRD_PARTY.md")
	if err != nil {
		t.Fatalf("ReadFile(THIRD_PARTY.md) error = %v", err)
	}
	for _, required := range []string{
		"v1.0.2", "h1:+27lYFPhQEhA9igtdOsJPRKYL/k3TwYsxBF5jr6KFv4=",
		"hmm/prob_emit.go", "fxsjy/jieba", "v0.30.0", "BSD-2-Clause",
		"Japanese", "generic IDF", "dynamic file-dictionary",
	} {
		if !strings.Contains(string(inventory), required) {
			t.Errorf("THIRD_PARTY.md missing %q", required)
		}
	}
	for _, license := range []string{"LICENSE-APACHE-2.0", "LICENSE-CEDAR-BSD-2-CLAUSE", "LICENSE-JIEBA-MIT"} {
		data, err := os.ReadFile(license)
		if err != nil {
			t.Errorf("ReadFile(%s) error = %v", license, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", license)
		}
	}

	entries, err := os.ReadDir("data")
	if err != nil {
		t.Fatalf("ReadDir(data) error = %v", err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Name())
	}
	wantNames := []string{"s_1.txt", "stop_tokens.txt", "t_1.txt"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("embedded data files = %#v, want only %#v", gotNames, wantNames)
	}
}
