package recommend

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/simp-lee/oxpio/internal/recommend/chinese"
)

func TestTokenizeNormalizationAndEngineeringTokens(t *testing.T) {
	got, err := Tokenize("Ｎｏｄｅ．ＪＳ foo,bar snake_case kebab-case path/to/file go:build C++ C# .NET")
	if err != nil {
		t.Fatalf("Tokenize() error = %v", err)
	}
	want := []string{
		"node.js", "foo", "bar", "snake_case", "kebab-case", "path/to/file",
		"go:build", "c++", "c#", "net",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize() = %#v, want %#v", got, want)
	}
}

func TestTokenizeChineseGoldensAndMixedOrder(t *testing.T) {
	got, err := Tokenize("Release 分布式数据库一致性协议 / 臺灣繁體中文資料庫設計")
	if err != nil {
		t.Fatalf("Tokenize() error = %v", err)
	}
	want := []string{
		"release", "分布式", "数据库", "一致性", "协议",
		"臺灣", "繁體中文", "資料庫", "設計",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize() = %#v, want %#v", got, want)
	}
}

func TestTokenizeInitializesChineseOnlyForHan(t *testing.T) {
	var calls atomic.Int32
	loader := func() (*chinese.Segmenter, error) {
		calls.Add(1)
		return chinese.Default()
	}

	nonHan, err := tokenize("Static かな カナ 한글 pipeline", loader)
	if err != nil {
		t.Fatalf("tokenize(non-Han) error = %v", err)
	}
	if want := []string{"static", "かな", "カナ", "한글", "pipeline"}; !reflect.DeepEqual(nonHan, want) {
		t.Fatalf("tokenize(non-Han) = %#v, want %#v", nonHan, want)
	}
	if calls.Load() != 0 {
		t.Fatalf("Chinese loader calls for non-Han input = %d, want 0", calls.Load())
	}

	han, err := tokenize("数据库 and 一致性", loader)
	if err != nil {
		t.Fatalf("tokenize(Han) error = %v", err)
	}
	if want := []string{"数据库", "一致性"}; !reflect.DeepEqual(han, want) {
		t.Fatalf("tokenize(Han) = %#v, want %#v", han, want)
	}
	if calls.Load() != 1 {
		t.Fatalf("Chinese loader calls for two Han spans = %d, want 1", calls.Load())
	}
}

func TestTokenizeReturnsChineseInitializationError(t *testing.T) {
	want := errors.New("load failed")
	got, err := tokenize("汉字", func() (*chinese.Segmenter, error) {
		return nil, want
	})
	if got != nil {
		t.Fatalf("tokenize() = %#v, want nil after initialization error", got)
	}
	if err != want {
		t.Fatalf("tokenize() error = %v, want original error %v", err, want)
	}
}

func TestStopwordsAndSingleHanFiltering(t *testing.T) {
	if stopwordSetVersion != "oxpio-related-stopwords-v1" {
		t.Fatalf("stopwordSetVersion = %q, want versioned v1 set", stopwordSetVersion)
	}

	got, err := Tokenize("the x a i 的 比如 以及 数据库 龙 C++")
	if err != nil {
		t.Fatalf("Tokenize() error = %v", err)
	}
	want := []string{"x", "a", "i", "数据库", "c++"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize(stopwords) = %#v, want %#v", got, want)
	}
}

func TestStopwordResourceIdentity(t *testing.T) {
	tests := []struct {
		name  string
		words map[string]struct{}
		count int
		want  string
	}{
		{name: "english", words: englishStopwordsV1, count: 124, want: "5f75c34b49090d105f64574def73b416c09734108c11297517b22e2b52b7e6fa"},
		{name: "supplemental", words: supplementalStopwordsV1, count: 20, want: "30c744ed9a2f87c5dcedb3a49c5471db6517189bc9cbd60dd881fdf77ca17af2"},
	}
	for _, test := range tests {
		if len(test.words) != test.count {
			t.Errorf("%s stopword count = %d, want %d", test.name, len(test.words), test.count)
		}
		if got := stopwordMembershipHash(test.words); got != test.want {
			t.Errorf("%s stopword membership SHA-256 = %s, want %s", test.name, got, test.want)
		}
	}
}

func stopwordMembershipHash(words map[string]struct{}) string {
	ordered := make([]string, 0, len(words))
	for word := range words {
		ordered = append(ordered, word)
	}
	sort.Strings(ordered)
	hasher := sha256.New()
	for _, word := range ordered {
		_, _ = hasher.Write([]byte(word))
		_, _ = hasher.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func TestTokenizerConcurrentDeterminism(t *testing.T) {
	const input = "Node.JS 分布式数据库一致性协议 C++ 臺灣繁體中文資料庫設計"
	want, err := Tokenize(input)
	if err != nil {
		t.Fatalf("Tokenize() error = %v", err)
	}

	const workers = 32
	var wait sync.WaitGroup
	errorsByWorker := make(chan []string, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 20; iteration++ {
				got, err := Tokenize(input)
				if err != nil || !reflect.DeepEqual(got, want) {
					errorsByWorker <- got
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for got := range errorsByWorker {
		t.Errorf("concurrent Tokenize() = %#v, want %#v", got, want)
	}
}
