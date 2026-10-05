package recommend

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
	"github.com/simp-lee/oxpio/internal/testutil/relatedfixture"
)

const relatedRSSHelperEnv = "OXPIO_RELATED_RSS_HELPER"

func TestRelatedPerformanceBudgets(t *testing.T) {
	if os.Getenv("OXPIO_RELATED_PERF") != "1" {
		t.Skip("set OXPIO_RELATED_PERF=1 on the fixed acceptance host")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"core", "adversarial"} {
		command := exec.Command(filepath.Join(root, "test", "verify-related-benchmarks.sh"), mode)
		command.Dir = root
		command.Env = os.Environ()
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s performance verification error = %v\n%s", mode, err, output)
		}
		t.Logf("%s", output)
	}
}

func TestRelatedMemoryLifetimes(t *testing.T) {
	if os.Getenv("OXPIO_RELATED_PROFILE_DIR") == "" {
		t.Skip("set OXPIO_RELATED_PROFILE_DIR to an external profile directory")
	}
	TestMemoryProfileCheckpoints(t)
}

func BenchmarkRelatedBuildWarm(b *testing.B) {
	cases := []struct {
		name  string
		kind  string
		count int
	}{
		{name: "mixed-500", kind: relatedfixture.CaseMixed, count: 500},
		{name: "mixed-1000", kind: relatedfixture.CaseMixed, count: 1000},
		{name: "mixed-5000", kind: relatedfixture.CaseMixed, count: 5000},
		{name: "sparse-posting-5000", kind: relatedfixture.CaseSparsePosting, count: 5000},
		{name: "term-40%-boundary-5000", kind: relatedfixture.CaseTermCoverage40, count: 5000},
		{name: "term-49%-coverage-5000", kind: relatedfixture.CaseTermCoverage49, count: 5000},
		{name: "tag-49%-coverage-5000", kind: relatedfixture.CaseTagCoverage49, count: 5000},
		{name: "rejected-content-5000", kind: relatedfixture.CaseRejectedContent, count: 5000},
	}
	for _, current := range cases {
		b.Run(current.name, func(b *testing.B) {
			fixture, err := relatedfixture.Generate(current.kind, current.count)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := Tokenize("分布式数据库一致性协议"); err != nil {
				b.Fatal(err)
			}
			parameters := ProductionEngineParameters(5, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				result, err := BuildEngine(fixture.Semantics, fixture.Index, fixture.Graph, parameters)
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(result)
			}
		})
	}
}

func TestPerformanceAdversarialBehavior(t *testing.T) {
	rejected, err := relatedfixture.Generate(relatedfixture.CaseRejectedContent, 100)
	if err != nil {
		t.Fatal(err)
	}
	features, err := BuildFeatureIndex(rejected.Semantics, ProductionEngineParameters(5, 1).Features)
	if err != nil {
		t.Fatal(err)
	}
	postingEntries := 0
	for _, posting := range features.Postings {
		postingEntries += len(posting)
	}
	if postingEntries == 0 {
		t.Fatal("rejected-content fixture generated no eligible posting candidates")
	}
	result, err := BuildEngine(rejected.Semantics, rejected.Index, rejected.Graph, ProductionEngineParameters(5, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range result.Documents {
		if len(document.Related) != 0 {
			t.Fatalf("rejected-content document %s retained %#v", document.RelPath, document.Related)
		}
	}

	termBoundary, err := relatedfixture.Generate(relatedfixture.CaseTermCoverage40, 100)
	if err != nil {
		t.Fatal(err)
	}
	boundaryFeatures, err := BuildFeatureIndex(termBoundary.Semantics, ProductionEngineParameters(5, 1).Features)
	if err != nil {
		t.Fatal(err)
	}
	boundaryTerms := 0
	for termID, term := range boundaryFeatures.Terms {
		if !strings.HasPrefix(term, "coverage-term-") {
			continue
		}
		boundaryTerms++
		if got := len(boundaryFeatures.Postings[termID]); got != 40 {
			t.Fatalf("40%% boundary posting %q length = %d, want 40", term, got)
		}
	}
	if boundaryTerms != 8 {
		t.Fatalf("40%% boundary retained terms = %d, want 8", boundaryTerms)
	}

	termCoverage, err := relatedfixture.Generate(relatedfixture.CaseTermCoverage49, 100)
	if err != nil {
		t.Fatal(err)
	}
	termFeatures, err := BuildFeatureIndex(termCoverage.Semantics, ProductionEngineParameters(5, 1).Features)
	if err != nil {
		t.Fatal(err)
	}
	for _, term := range termFeatures.Terms {
		if strings.HasPrefix(term, "coverage-term-") {
			t.Fatalf("49%% coverage term %q bypassed frozen 40%% cutoff", term)
		}
	}
}

func TestRSSHelperIsolation(t *testing.T) {
	if specification := os.Getenv(relatedRSSHelperEnv); specification != "" {
		kind, count, err := parseRelatedFixtureSpecification(specification)
		if err != nil {
			t.Fatal(err)
		}
		fixture, err := relatedfixture.Generate(kind, count)
		if err != nil {
			t.Fatal(err)
		}
		result, err := BuildEngine(fixture.Semantics, fixture.Index, fixture.Graph, ProductionEngineParameters(5, 0))
		if err != nil {
			t.Fatal(err)
		}
		runtime.KeepAlive(result)
		peak, err := readVmHWM()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("RELATED_RSS %s %d\n", kind, peak)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestRSSHelperIsolation$", "-test.count=1")
	command.Env = append(os.Environ(), relatedRSSHelperEnv+"="+relatedfixture.CaseMixed+":20")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("RSS helper error = %v\n%s", err, output)
	}
	if strings.Count(string(output), "RELATED_RSS ") != 1 {
		t.Fatalf("RSS helper output = %q, want exactly one case", output)
	}
}

func TestMemoryProfileCheckpoints(t *testing.T) {
	previousProfileRate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = previousProfileRate }()

	profileDir := os.Getenv("OXPIO_RELATED_PROFILE_DIR")
	if profileDir == "" {
		profileDir = t.TempDir()
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	fixture, err := relatedfixture.Generate(relatedfixture.CaseMixed, 20)
	if err != nil {
		t.Fatal(err)
	}
	profileCount := 0
	profiledObserver := func(_ int, _ int) {
		switch profileCount {
		case 0:
			runtime.GC()
			writeRelatedHeapProfile(t, profileDir, "post-tokenization.pprof")
		case 1:
			runtime.GC()
			writeRelatedHeapProfile(t, profileDir, "during-scoring.pprof")
		}
		profileCount++
	}
	ranking, err := buildEngineFromSemanticOwner(&fixture.Semantics, fixture.Index, fixture.Graph, ProductionEngineParameters(5, 1), profiledObserver)
	if err != nil {
		t.Fatal(err)
	}
	if profileCount < 2 {
		t.Fatalf("scoring profile callbacks = %d, want at least 2", profileCount)
	}

	runtime.GC()
	writeRelatedHeapProfile(t, profileDir, "page-output.pprof")
	runtime.KeepAlive(ranking)
	writeRetainedOwnerControlProfile(t, profileDir)
	for _, name := range []string{"post-tokenization.pprof", "during-scoring.pprof", "page-output.pprof", "retained-owner-control.pprof"} {
		info, err := os.Stat(filepath.Join(profileDir, name))
		if err != nil || info.Size() == 0 {
			t.Fatalf("profile %s missing/empty: %v", name, err)
		}
	}
}

func buildEngineFromSemanticOwner(owner *[]model.RelatedSemanticDocument, idx *model.VaultIndex, graph *model.LinkGraph, parameters EngineParameters, observer pairObserver) (*EngineResult, error) {
	return buildOwnedEngine(owner, idx, graph, parameters, observer)
}

func writeRetainedOwnerControlProfile(t *testing.T, profileDir string) {
	t.Helper()
	fixture, err := relatedfixture.Generate(relatedfixture.CaseMixed, 20)
	if err != nil {
		t.Fatal(err)
	}
	counts := newDocumentTermCountOwner(64)
	for index := range counts {
		counts[index].terms = newTermFieldCountOwner()
		counts[index].terms[fmt.Sprintf("retained-%d", index)] = termFieldCounts{body: 1}
	}
	documentFrequency := newDocumentFrequencyOwner()
	documentFrequency["retained"] = len(counts)
	termIDs := newTermIDOwner(64)
	termIDs["retained"] = 0
	selected := newSelectedFeatureOwner(64)
	selected[0] = []weightedTerm{{term: "retained", weight: 1}}
	selectedDF := newSelectedDFOwner(64)
	selectedDF[0] = 2

	runtime.GC()
	writeRelatedHeapProfile(t, profileDir, "retained-owner-control.pprof")
	runtime.KeepAlive(fixture.Semantics)
	runtime.KeepAlive(counts)
	runtime.KeepAlive(documentFrequency)
	runtime.KeepAlive(termIDs)
	runtime.KeepAlive(selected)
	runtime.KeepAlive(selectedDF)
}

func parseRelatedFixtureSpecification(value string) (string, int, error) {
	kind, countText, ok := strings.Cut(value, ":")
	if !ok {
		return "", 0, fmt.Errorf("invalid fixture specification %q", value)
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count < 0 {
		return "", 0, fmt.Errorf("invalid fixture count %q", countText)
	}
	return kind, count, nil
}

func readVmHWM() (value int64, err error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "VmHWM:" {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("VmHWM not found")
}

func writeRelatedHeapProfile(t *testing.T, directory string, name string) {
	t.Helper()
	file, err := os.Create(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.WriteHeapProfile(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
