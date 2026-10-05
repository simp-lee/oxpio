package asset

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/simp-lee/oxpio/internal/model"
)

func mustNewCollector(t *testing.T, vaultRoot string, indexed map[string]*model.Asset) *AssetCollector {
	t.Helper()

	collector, err := newCollectorWithReservedPaths(vaultRoot, indexed, nil, nil, nil)
	if err != nil {
		t.Fatalf("newCollectorWithReservedPaths() error = %v", err)
	}

	return collector
}

func mustNewCollectorWithHook(t *testing.T, vaultRoot string, indexed map[string]*model.Asset, scanInventoryHook func() error) *AssetCollector {
	t.Helper()

	collector, err := newCollectorWithReservedPaths(vaultRoot, indexed, nil, nil, scanInventoryHook)
	if err != nil {
		t.Fatalf("newCollectorWithReservedPaths() error = %v", err)
	}

	return collector
}

func mustNewCollectorWithResourceFiles(t *testing.T, vaultRoot string, indexed map[string]*model.Asset, resourceFiles []string) *AssetCollector {
	t.Helper()

	collector, err := NewCollectorWithResourceFiles(vaultRoot, indexed, nil, resourceFiles)
	if err != nil {
		t.Fatalf("NewCollectorWithResourceFiles() error = %v", err)
	}

	return collector
}

func mergeAssetsForTest(vaultRoot string, indexed map[string]*model.Asset, collector *AssetCollector) map[string]*model.Asset {
	return mergeAssetsForTestWithReservedPaths(vaultRoot, indexed, collector, nil)
}

func mergeAssetsForTestWithReservedPaths(vaultRoot string, indexed map[string]*model.Asset, collector *AssetCollector, reservedOutputPaths []string) map[string]*model.Asset {
	merged := make(map[string]*model.Asset, len(indexed))
	mergeAssetsIntoTestMap(merged, indexed)
	if collector != nil {
		mergeAssetsIntoTestMap(merged, collector.Snapshot())
	}

	assigned := planAssetDestinations(vaultRoot, merged, normalizeReservedOutputKeys(reservedOutputPaths))
	for srcPath, asset := range merged {
		if asset == nil {
			continue
		}
		if dstPath := assigned[srcPath]; dstPath != "" {
			asset.DstPath = dstPath
		}
	}

	return merged
}

func mergeAssetsIntoTestMap(dst map[string]*model.Asset, src map[string]*model.Asset) {
	if len(src) == 0 {
		return
	}

	for key, asset := range src {
		srcPath := normalizeAssetSource(key, asset)
		if srcPath == "" {
			continue
		}

		existing := dst[srcPath]
		if existing == nil {
			existing = &model.Asset{SrcPath: srcPath}
			dst[srcPath] = existing
		}

		if asset != nil {
			existing.RefCount += asset.RefCount
			if dstPath := outputSitePath(asset.DstPath); dstPath != "" {
				existing.DstPath = dstPath
			}
		}
	}
}

func TestPlanDataEscapesLiteralPercentTriplets(t *testing.T) {
	t.Parallel()

	planned := PlanData(".oxpio/theme/assets/icon%2Fmark.png", []byte("icon"))
	if !strings.HasPrefix(planned.DstPath, "assets/icon%252fmark.") {
		t.Fatalf("PlanData().DstPath = %q, want literal percent escaped for the URL", planned.DstPath)
	}
	decoded, err := url.PathUnescape(planned.DstPath)
	if err != nil {
		t.Fatal(err)
	}
	if pathDir := filepath.ToSlash(filepath.Dir(decoded)); pathDir != "assets" {
		t.Fatalf("decoded output directory = %q, want assets", pathDir)
	}
}

func TestHasImageExtensionRecognizesEscapedSuffix(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"missing%2Epng", "images/diagram%2ESVG?raw=1#preview"} {
		if !HasImageExtension(target) {
			t.Fatalf("HasImageExtension(%q) = false, want true", target)
		}
	}
}

func TestResolvePathAttachmentFallbackOnlyForBarePaths(t *testing.T) {
	t.Parallel()

	note := &model.Note{RelPath: "notes/chapter/current.md"}
	attachmentPath := "attachments/poster.png"

	tests := []struct {
		name                    string
		rawDestination          string
		want                    string
		wantAttachmentCandidate bool
	}{
		{
			name:                    "bare_filename",
			rawDestination:          "poster.png",
			want:                    attachmentPath,
			wantAttachmentCandidate: true,
		},
		{
			name:                    "vault_root_path",
			rawDestination:          "/poster.png",
			want:                    "",
			wantAttachmentCandidate: false,
		},
		{
			name:                    "note_relative_path",
			rawDestination:          "./poster.png",
			want:                    "",
			wantAttachmentCandidate: false,
		},
		{
			name:                    "parent_relative_path",
			rawDestination:          "../poster.png",
			want:                    "",
			wantAttachmentCandidate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidates := CandidatePaths(note, "attachments", tt.rawDestination)
			hasAttachmentCandidate := false
			for _, candidate := range candidates {
				if candidate == attachmentPath {
					hasAttachmentCandidate = true
					break
				}
			}
			if hasAttachmentCandidate != tt.wantAttachmentCandidate {
				t.Fatalf("CandidatePaths(%q) attachment candidate = %v, want %v (candidates=%v)", tt.rawDestination, hasAttachmentCandidate, tt.wantAttachmentCandidate, candidates)
			}

			got := ""
			for _, candidate := range candidates {
				if candidate == attachmentPath {
					got = candidate
					break
				}
			}
			if got != tt.want {
				t.Fatalf("resolved candidate for %q = %q, want %q", tt.rawDestination, got, tt.want)
			}
		})
	}
}

func TestAssetCollectorRegisterConcurrent(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/hero.png", "hero")
	indexed := map[string]*model.Asset{
		"images/hero.png": {SrcPath: "images/hero.png", RefCount: 1},
	}

	collector := mustNewCollector(t, vaultRoot, indexed)

	const registrations = 32
	expected := expectedHashedAssetPath(t, vaultRoot, "images/hero.png")
	results := make(chan string, registrations)
	var wg sync.WaitGroup
	for range registrations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- collector.Register("images/hero.png")
		}()
	}
	wg.Wait()
	close(results)

	for sitePath := range results {
		if sitePath != expected {
			t.Fatalf("Register() = %q, want %q", sitePath, expected)
		}
	}

	snapshot := collector.Snapshot()
	asset := snapshot["images/hero.png"]
	if asset == nil {
		t.Fatal("Snapshot()[images/hero.png] = nil, want asset")
	}
	if asset.RefCount != registrations {
		t.Fatalf("asset.RefCount = %d, want %d", asset.RefCount, registrations)
	}
	if asset.DstPath != expected {
		t.Fatalf("asset.DstPath = %q, want %q", asset.DstPath, expected)
	}
}

func TestAssetCollectorRegisterRejectsScanExcludedInputsAndKeepsVisiblePathPlain(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "left")
	writeAssetFile(t, vaultRoot, ".hidden/attachments/photo.png", "right")
	writeAssetFile(t, vaultRoot, ".obsidian/assets/photo.png", "obsidian")
	writeAssetFile(t, vaultRoot, "node_modules/pkg/photo.png", "node")
	writeAssetFile(t, vaultRoot, "oxpio.yaml", "passwordHash: secret")
	writeAssetFile(t, vaultRoot, ".git/config", "url = https://user:secret@example.test/repo.git")

	indexed := map[string]*model.Asset{
		"images/photo.png": {SrcPath: "images/photo.png", RefCount: 1},
	}

	collector := mustNewCollector(t, vaultRoot, indexed)
	visible := collector.Register("images/photo.png")
	wantVisible := expectedHashedAssetPath(t, vaultRoot, "images/photo.png")
	if visible != wantVisible {
		t.Fatalf("Register(images/photo.png) = %q, want %q", visible, wantVisible)
	}

	for _, input := range []string{
		".obsidian/assets/photo.png",
		"node_modules/pkg/photo.png",
		"oxpio.yaml",
		".git/config",
	} {
		if got := collector.Register(input); got != "" {
			t.Fatalf("Register(%q) = %q, want empty path for scan-excluded input", input, got)
		}
	}

	if got := collector.Register("images/photo.png"); got != visible {
		t.Fatalf("second Register(images/photo.png) = %q, want stable visible path %q", got, visible)
	}

	snapshot := collector.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("len(Snapshot()) = %d, want 1 accepted asset", len(snapshot))
	}
	if asset := snapshot["images/photo.png"]; asset == nil || asset.DstPath != visible || asset.RefCount != 2 {
		t.Fatalf("Snapshot()[images/photo.png] = %#v, want plain visible asset with refcount 2", asset)
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, collector)
	if len(merged) != 1 {
		t.Fatalf("len(MergeAssets()) = %d, want 1 accepted asset", len(merged))
	}
	if asset := merged["images/photo.png"]; asset == nil || asset.DstPath != visible {
		t.Fatalf("merged[images/photo.png] = %#v, want stable plain DstPath %q", asset, visible)
	}
}

func TestAssetCollectorRegisterBuildsVaultInventoryOnce(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "photo")
	writeAssetFile(t, vaultRoot, "files/manual.pdf", "manual")
	writeAssetFile(t, vaultRoot, "docs/guide.txt", "guide")

	scans := 0
	collector := mustNewCollectorWithHook(t, vaultRoot, nil, func() error {
		scans++
		return nil
	})
	if scans != 1 {
		t.Fatalf("inventory scans after construction = %d, want 1", scans)
	}

	for srcPath := range map[string]struct{}{
		"images/photo.png": {},
		"files/manual.pdf": {},
		"docs/guide.txt":   {},
	} {
		want := expectedHashedAssetPath(t, vaultRoot, srcPath)
		if got := collector.Register(srcPath); got != want {
			t.Fatalf("Register(%q) = %q, want %q", srcPath, got, want)
		}
	}

	if scans != 1 {
		t.Fatalf("inventory scans after repeated Register calls = %d, want 1", scans)
	}
}

func TestNewCollectorWithReservedPathsReturnsInventoryScanError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("scan failed")
	collector, err := newCollectorWithReservedPaths(t.TempDir(), nil, nil, nil, func() error {
		return wantErr
	})
	if collector != nil {
		t.Fatalf("newCollectorWithReservedPaths() = %#v, want nil collector on scan failure", collector)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("newCollectorWithReservedPaths() error = %v, want wrapped %v", err, wantErr)
	}
}

func TestAssetCollectorRegisterKeepsPlainPathForUniquePass2AssetWithoutIndexedPlan(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/hero.png", "hero")

	collector := mustNewCollector(t, vaultRoot, nil)
	got := collector.Register("images/hero.png")
	want := expectedHashedAssetPath(t, vaultRoot, "images/hero.png")
	if got != want {
		t.Fatalf("Register() = %q, want %q", got, want)
	}
}

func TestAssetCollectorRegisterHashesMissingPass2AssetWithoutIndexedPlan(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()

	collector := mustNewCollector(t, vaultRoot, nil)
	got := collector.Register("images/missing.png")
	want := expectedHashedAssetPath(t, vaultRoot, "images/missing.png")
	if got != want {
		t.Fatalf("Register() = %q, want %q", got, want)
	}
}

func TestMergeAssetsDedupesAcrossPasses(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/hero.png", "hero")
	writeAssetFile(t, vaultRoot, "files/manual.pdf", "manual")
	writeAssetFile(t, vaultRoot, "files/extra.bin", "extra")

	indexed := map[string]*model.Asset{
		"images/hero.png":  {SrcPath: "images/hero.png", RefCount: 1},
		"files/manual.pdf": {SrcPath: "files/manual.pdf", RefCount: 2},
		"files/extra.bin":  {SrcPath: "files/extra.bin", RefCount: 1},
	}
	collector := mustNewCollector(t, vaultRoot, indexed)
	heroPath := collector.Register("images/hero.png")
	extraPath := collector.Register("files/extra.bin")
	collector.Register("files/extra.bin")

	merged := mergeAssetsForTest(vaultRoot, indexed, collector)
	if len(merged) != 3 {
		t.Fatalf("len(MergeAssets()) = %d, want 3", len(merged))
	}

	if asset := merged["images/hero.png"]; asset == nil || asset.RefCount != 2 || asset.DstPath != heroPath {
		t.Fatalf("merged[images/hero.png] = %#v, want deduped asset with refcount 2", asset)
	}
	if asset := merged["files/manual.pdf"]; asset == nil || asset.RefCount != 2 || asset.DstPath != expectedHashedAssetPath(t, vaultRoot, "files/manual.pdf") {
		t.Fatalf("merged[files/manual.pdf] = %#v, want pass-1-only asset preserved", asset)
	}
	if asset := merged["files/extra.bin"]; asset == nil || asset.RefCount != 3 || asset.DstPath != extraPath {
		t.Fatalf("merged[files/extra.bin] = %#v, want merged asset with stable plain path", asset)
	}
}

func TestMergeAssetsHashesSameBasenameCollisions(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "left")
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "right")

	indexed := map[string]*model.Asset{
		"images/photo.png":      {SrcPath: "images/photo.png", RefCount: 1},
		"attachments/photo.png": {SrcPath: "attachments/photo.png", RefCount: 1},
	}
	collector := mustNewCollector(t, vaultRoot, indexed)
	registered := collector.Register("attachments/photo.png")
	if registered == "assets/photo.png" {
		t.Fatalf("Register() = %q, want hashed collision path", registered)
	}
	if !strings.HasPrefix(registered, "assets/photo.") || !strings.HasSuffix(registered, ".png") {
		t.Fatalf("Register() = %q, want assets/photo.<hash>.png", registered)
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, collector)
	left := merged["images/photo.png"]
	right := merged["attachments/photo.png"]
	if left == nil || right == nil {
		t.Fatalf("merged collision assets = %#v, want both assets", merged)
	}
	if left.DstPath == "assets/photo.png" || right.DstPath == "assets/photo.png" {
		t.Fatalf("collision DstPaths = %q, %q, want hashed outputs for both assets", left.DstPath, right.DstPath)
	}
	if left.DstPath == right.DstPath {
		t.Fatalf("collision DstPaths = %q and %q, want different hashes for different file contents", left.DstPath, right.DstPath)
	}
	if right.DstPath != registered {
		t.Fatalf("right.DstPath = %q, want stable registered path %q", right.DstPath, registered)
	}
}

func TestAssetCollectorRegisterReplansIndexedPlainPathWhenPass2CollisionExists(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "left")
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "right")

	indexed := map[string]*model.Asset{
		"images/photo.png": {SrcPath: "images/photo.png", RefCount: 1},
	}
	collector := mustNewCollector(t, vaultRoot, indexed)
	left := collector.Register("images/photo.png")
	if left == "assets/photo.png" {
		t.Fatalf("Register(images/photo.png) = %q, want hashed collision path", left)
	}
	if !strings.HasPrefix(left, "assets/photo.") || !strings.HasSuffix(left, ".png") {
		t.Fatalf("Register(images/photo.png) = %q, want assets/photo.<hash>.png", left)
	}

	right := collector.Register("attachments/photo.png")
	if right == "assets/photo.png" {
		t.Fatalf("Register(attachments/photo.png) = %q, want hashed collision path", right)
	}
	if !strings.HasPrefix(right, "assets/photo.") || !strings.HasSuffix(right, ".png") {
		t.Fatalf("Register(attachments/photo.png) = %q, want assets/photo.<hash>.png", right)
	}
	if left == right {
		t.Fatalf("Register() returned %q and %q, want distinct hashed paths for different contents", left, right)
	}

	snapshot := collector.Snapshot()
	if asset := snapshot["images/photo.png"]; asset == nil || asset.DstPath != left {
		t.Fatalf("Snapshot()[images/photo.png] = %#v, want stable DstPath %q", asset, left)
	}
	if asset := snapshot["attachments/photo.png"]; asset == nil || asset.DstPath != right {
		t.Fatalf("Snapshot()[attachments/photo.png] = %#v, want stable DstPath %q", asset, right)
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, collector)
	if asset := merged["images/photo.png"]; asset == nil || asset.DstPath != left {
		t.Fatalf("merged[images/photo.png] = %#v, want stable DstPath %q", asset, left)
	}
	if asset := merged["attachments/photo.png"]; asset == nil || asset.DstPath != right {
		t.Fatalf("merged[attachments/photo.png] = %#v, want stable DstPath %q", asset, right)
	}
}

func TestAssetCollectorRegisterHashesPass2OnlyCollisionIndependentlyOfOrder(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "left")
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "right")

	collectorA := mustNewCollector(t, vaultRoot, nil)
	forward := map[string]string{
		"images/photo.png":      collectorA.Register("images/photo.png"),
		"attachments/photo.png": collectorA.Register("attachments/photo.png"),
	}

	collectorB := mustNewCollector(t, vaultRoot, nil)
	reverse := map[string]string{
		"attachments/photo.png": collectorB.Register("attachments/photo.png"),
		"images/photo.png":      collectorB.Register("images/photo.png"),
	}

	for srcPath, sitePath := range forward {
		if sitePath == "assets/photo.png" {
			t.Fatalf("forward Register(%q) = %q, want hashed collision path", srcPath, sitePath)
		}
		if !strings.HasPrefix(sitePath, "assets/photo.") || !strings.HasSuffix(sitePath, ".png") {
			t.Fatalf("forward Register(%q) = %q, want assets/photo.<hash>.png", srcPath, sitePath)
		}
		if reverse[srcPath] != sitePath {
			t.Fatalf("Register(%q) = %q in forward order, %q in reverse order, want order-independent site path", srcPath, sitePath, reverse[srcPath])
		}
	}
}

func TestAssetCollectorRegisterKeepsExistingPathPlainAfterMissingSameBasename(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "photo")

	collector := mustNewCollector(t, vaultRoot, nil)
	missing := collector.Register("images/photo.png")
	wantMissing := expectedHashedAssetPath(t, vaultRoot, "images/photo.png")
	if missing != wantMissing {
		t.Fatalf("Register(images/photo.png) = %q, want %q", missing, wantMissing)
	}

	existing := collector.Register("attachments/photo.png")
	wantExisting := expectedHashedAssetPath(t, vaultRoot, "attachments/photo.png")
	if existing != wantExisting {
		t.Fatalf("Register(attachments/photo.png) = %q, want %q", existing, wantExisting)
	}

	merged := mergeAssetsForTest(vaultRoot, nil, collector)
	if asset := merged["attachments/photo.png"]; asset == nil || asset.DstPath != existing {
		t.Fatalf("merged[attachments/photo.png] = %#v, want stable plain DstPath %q", asset, existing)
	}
	if asset := merged["images/photo.png"]; asset == nil || asset.DstPath == "assets/photo.png" || !strings.HasPrefix(asset.DstPath, "assets/photo.") || !strings.HasSuffix(asset.DstPath, ".png") {
		t.Fatalf("merged[images/photo.png] = %#v, want hashed DstPath distinct from %q", asset, existing)
	}
}

func TestAssetCollectorRegisterKeepsIndexedCollisionStable(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "left")
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "right")

	indexed := map[string]*model.Asset{
		"images/photo.png":      {SrcPath: "images/photo.png", RefCount: 1},
		"attachments/photo.png": {SrcPath: "attachments/photo.png", RefCount: 1},
	}
	collector := mustNewCollector(t, vaultRoot, indexed)
	left := collector.Register("images/photo.png")
	right := collector.Register("attachments/photo.png")

	if left == "assets/photo.png" || right == "assets/photo.png" {
		t.Fatalf("Register() returned %q and %q, want hashed collision paths for both sources", left, right)
	}
	if left == right {
		t.Fatalf("Register() returned %q and %q, want distinct paths for different contents", left, right)
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, collector)
	if asset := merged["images/photo.png"]; asset == nil || asset.DstPath != left {
		t.Fatalf("merged[images/photo.png] = %#v, want stable DstPath %q", asset, left)
	}
	if asset := merged["attachments/photo.png"]; asset == nil || asset.DstPath != right {
		t.Fatalf("merged[attachments/photo.png] = %#v, want stable DstPath %q", asset, right)
	}
}

func TestAssetCollectorPlanDestinationsIgnoresUnreferencedCollisionPeerWhenInventoryIsEmitScoped(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "attachment")
	writeAssetFile(t, vaultRoot, "images/photo.png", "unreferenced-collision")

	collector := mustNewCollectorWithResourceFiles(t, vaultRoot, nil, nil)
	planned := collector.PlanDestinations(map[string]*model.Asset{
		"attachments/photo.png": {SrcPath: "attachments/photo.png", RefCount: 1},
	})
	if len(planned) != 1 {
		t.Fatalf("len(PlanDestinations()) = %d, want 1 requested asset", len(planned))
	}

	dstPath := planned["attachments/photo.png"]
	wantPath := expectedHashedAssetPath(t, vaultRoot, "attachments/photo.png")
	if dstPath != wantPath {
		t.Fatalf("PlanDestinations()[attachments/photo.png] = %q, want %q when only an unreferenced peer exists", dstPath, wantPath)
	}

	if got := collector.Register("attachments/photo.png"); got != dstPath {
		t.Fatalf("Register(attachments/photo.png) = %q, want finalized planned path %q", got, dstPath)
	}
	if snapshot := collector.Snapshot(); snapshot["attachments/photo.png"] == nil || snapshot["attachments/photo.png"].DstPath != dstPath {
		t.Fatalf("Snapshot()[attachments/photo.png] = %#v, want finalized planned path %q", snapshot["attachments/photo.png"], dstPath)
	}
}

func TestMergeAssetsRecomputesCollisionHashesWhenAssetContentChanges(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "left-v1")
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "right")

	indexed := mergeAssetsForTest(vaultRoot, map[string]*model.Asset{
		"images/photo.png":      {SrcPath: "images/photo.png", RefCount: 1},
		"attachments/photo.png": {SrcPath: "attachments/photo.png", RefCount: 1},
	}, nil)
	initialLeft := indexed["images/photo.png"]
	if initialLeft == nil {
		t.Fatal("indexed[images/photo.png] = nil, want asset")
	}
	if initialLeft.DstPath == "assets/photo.png" {
		t.Fatalf("indexed[images/photo.png].DstPath = %q, want hashed collision path", initialLeft.DstPath)
	}
	initialLeftDst := initialLeft.DstPath

	writeAssetFile(t, vaultRoot, "images/photo.png", "left-v2")

	merged := mergeAssetsForTest(vaultRoot, indexed, nil)
	sources := []string{"attachments/photo.png", "images/photo.png"}
	want := hashCollisionPaths(vaultRoot, plainAssetKey("images/photo.png"), sources)

	left := merged["images/photo.png"]
	if left == nil {
		t.Fatal("merged[images/photo.png] = nil, want asset")
	}
	if left.DstPath != want["images/photo.png"] {
		t.Fatalf("merged[images/photo.png].DstPath = %q, want recomputed path %q", left.DstPath, want["images/photo.png"])
	}
	if left.DstPath == initialLeftDst {
		t.Fatalf("merged[images/photo.png].DstPath = %q, want content change to produce a new hashed destination", left.DstPath)
	}

	right := merged["attachments/photo.png"]
	if right == nil {
		t.Fatal("merged[attachments/photo.png] = nil, want asset")
	}
	if right.DstPath != want["attachments/photo.png"] {
		t.Fatalf("merged[attachments/photo.png].DstPath = %q, want current collision-group path %q", right.DstPath, want["attachments/photo.png"])
	}
}

func TestMergeAssetsRestoresPlainPathAfterCollisionDisappears(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "photo")

	indexed := map[string]*model.Asset{
		"images/photo.png": {
			SrcPath:  "images/photo.png",
			RefCount: 1,
			DstPath:  expectedHashedAssetPath(t, vaultRoot, "images/photo.png"),
		},
		"attachments/photo.png": {
			SrcPath:  "attachments/photo.png",
			RefCount: 1,
			DstPath:  expectedHashedAssetPath(t, vaultRoot, "attachments/photo.png"),
		},
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, nil)
	wantAttachment := expectedHashedAssetPath(t, vaultRoot, "attachments/photo.png")
	if asset := merged["attachments/photo.png"]; asset == nil || asset.DstPath != wantAttachment {
		t.Fatalf("merged[attachments/photo.png] = %#v, want content-addressed DstPath %q", asset, wantAttachment)
	}
	if asset := merged["images/photo.png"]; asset == nil || asset.DstPath == "assets/photo.png" || !strings.HasPrefix(asset.DstPath, "assets/photo.") || !strings.HasSuffix(asset.DstPath, ".png") {
		t.Fatalf("merged[images/photo.png] = %#v, want hashed DstPath after surviving source reclaims plain path", asset)
	}
}

func TestMergeAssetsRestoresPlainPathAfterReservedOutputIsReleased(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "theme/custom.css", "body {}")

	reserved := mergeAssetsForTestWithReservedPaths(vaultRoot, map[string]*model.Asset{
		"theme/custom.css": {
			SrcPath:  "theme/custom.css",
			RefCount: 1,
		},
	}, nil, []string{"assets/custom.css"})
	if asset := reserved["theme/custom.css"]; asset == nil || asset.DstPath == "assets/custom.css" {
		t.Fatalf("reserved[theme/custom.css] = %#v, want hashed DstPath while plain output is reserved", asset)
	}

	released := mergeAssetsForTest(vaultRoot, reserved, nil)
	wantReleased := expectedHashedAssetPath(t, vaultRoot, "theme/custom.css")
	if asset := released["theme/custom.css"]; asset == nil || asset.DstPath != wantReleased {
		t.Fatalf("released[theme/custom.css] = %#v, want content-addressed DstPath %q after reserved output is removed", asset, wantReleased)
	}
}

func TestAssetPlannerAvoidsReservedHashedDestination(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	srcPath := "images/hero.png"
	writeAssetFile(t, vaultRoot, srcPath, "hero")
	contentAddressed := expectedHashedAssetPath(t, vaultRoot, srcPath)

	assigned := planAssetDestinations(vaultRoot, map[string]*model.Asset{
		srcPath: {SrcPath: srcPath},
	}, normalizeReservedOutputKeys([]string{contentAddressed}))
	got := assigned[srcPath]
	if got == "" {
		t.Fatalf("planAssetDestinations()[%q] = empty path, want destination", srcPath)
	}
	if got == contentAddressed {
		t.Fatalf("planAssetDestinations()[%q] = %q, want reserved destination to be avoided", srcPath, got)
	}
	if isReservedOutputKey(got, normalizeReservedOutputKeys([]string{contentAddressed})) {
		t.Fatalf("planAssetDestinations()[%q] = %q, want non-reserved destination", srcPath, got)
	}

	collector, err := NewCollectorWithResourceFiles(vaultRoot, nil, []string{contentAddressed}, nil)
	if err != nil {
		t.Fatalf("NewCollectorWithResourceFiles() error = %v", err)
	}
	if got := collector.Register(srcPath); got != assigned[srcPath] {
		t.Fatalf("Register(%q) = %q, want planner destination %q", srcPath, got, assigned[srcPath])
	}

	missingSource := "images/missing.png"
	missingDestination := hashedAssetPath(missingSource, missingAssetHash(missingSource))
	missingCollector, err := NewCollectorWithResourceFiles(vaultRoot, nil, []string{missingDestination}, nil)
	if err != nil {
		t.Fatalf("NewCollectorWithResourceFiles() for missing source error = %v", err)
	}
	if got := missingCollector.Register(missingSource); got == missingDestination || isReservedOutputKey(got, normalizeReservedOutputKeys([]string{missingDestination})) {
		t.Fatalf("Register(%q) = %q, want non-reserved fallback destination", missingSource, got)
	}
}

func TestMergeAssetsRewritesNonAssetDestinationUnderAssets(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/hero.png", "hero-bytes")

	indexed := map[string]*model.Asset{
		"images/hero.png": {SrcPath: "images/hero.png", RefCount: 1, DstPath: "notes/hero.png"},
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, nil)
	asset := merged["images/hero.png"]
	if asset == nil {
		t.Fatal("merged[images/hero.png] = nil, want asset")
	}
	if asset.DstPath != expectedHashedAssetPath(t, vaultRoot, "images/hero.png") {
		t.Fatalf("asset.DstPath = %q, want content-addressed path", asset.DstPath)
	}
}

func TestAssetCollectorRegisterRejectsWindowsAbsoluteAndUNCPaths(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"C:/Windows/System32/drivers/etc/hosts",
		"C:\\Windows\\System32\\drivers\\etc\\hosts",
		"//server/share/photo.png",
		"\\\\server\\share\\photo.png",
	} {
		t.Run(input, func(t *testing.T) {
			collector := mustNewCollector(t, t.TempDir(), nil)
			if got := collector.Register(input); got != "" {
				t.Fatalf("Register(%q) = %q, want empty path", input, got)
			}
			if snapshot := collector.Snapshot(); len(snapshot) != 0 {
				t.Fatalf("len(Snapshot()) = %d, want 0", len(snapshot))
			}
		})
	}
}

func TestAssetCollectorRegisterRejectsSchemeBasedPaths(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"http://example.com/photo.png",
		"https://example.com/photo.png",
		"data:image/png;base64,AAAA",
		"file:///tmp/photo.png",
		"mailto:foo@example.com",
	} {
		t.Run(input, func(t *testing.T) {
			collector := mustNewCollector(t, t.TempDir(), nil)
			if got := collector.Register(input); got != "" {
				t.Fatalf("Register(%q) = %q, want empty path", input, got)
			}
			if snapshot := collector.Snapshot(); len(snapshot) != 0 {
				t.Fatalf("len(Snapshot()) = %d, want 0", len(snapshot))
			}
		})
	}
}

func TestAssetCollectorRegisterTreatsSymlinkedSourceAsMissing(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	externalRoot := t.TempDir()
	writeAssetFile(t, externalRoot, "hero.png", "outside")
	writeAssetSymlinkOrSkip(t, filepath.Join(externalRoot, "hero.png"), filepath.Join(vaultRoot, "images", "hero.png"))

	collector := mustNewCollector(t, vaultRoot, nil)
	got := collector.Register("images/hero.png")
	want := expectedHashedAssetPath(t, vaultRoot, "images/hero.png")
	if got != want {
		t.Fatalf("Register(images/hero.png) = %q, want %q", got, want)
	}
}

func TestAssetCollectorRegisterReturnsStableSitePath(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/photo.png", "left")
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "right")

	indexed := map[string]*model.Asset{
		"attachments/photo.png": {SrcPath: "attachments/photo.png", RefCount: 1},
		"images/photo.png":      {SrcPath: "images/photo.png", RefCount: 1},
	}
	collector := mustNewCollector(t, vaultRoot, indexed)
	first := collector.Register("images/photo.png")
	second := collector.Register("images/photo.png")
	if first != second {
		t.Fatalf("Register() returned %q then %q, want stable site path", first, second)
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, collector)
	if asset := merged["images/photo.png"]; asset == nil || asset.DstPath != first || asset.RefCount != 3 {
		t.Fatalf("merged[images/photo.png] = %#v, want stable DstPath %q and refcount 3", asset, first)
	}
}

func TestAssetCollectorRegisterHashesCaseInsensitiveBasenameCollisions(t *testing.T) {
	t.Parallel()

	vaultRoot := t.TempDir()
	writeAssetFile(t, vaultRoot, "images/Photo.png", "left")
	writeAssetFile(t, vaultRoot, "attachments/photo.png", "right")

	indexed := map[string]*model.Asset{
		"images/Photo.png":      {SrcPath: "images/Photo.png", RefCount: 1},
		"attachments/photo.png": {SrcPath: "attachments/photo.png", RefCount: 1},
	}
	collector := mustNewCollector(t, vaultRoot, indexed)
	left := collector.Register("images/Photo.png")
	right := collector.Register("attachments/photo.png")

	if left == "assets/Photo.png" || left == "assets/photo.png" {
		t.Fatalf("Register(images/Photo.png) = %q, want hashed collision path", left)
	}
	if right == "assets/Photo.png" || right == "assets/photo.png" {
		t.Fatalf("Register(attachments/photo.png) = %q, want hashed collision path", right)
	}
	if !strings.HasPrefix(left, "assets/photo.") || !strings.HasSuffix(left, ".png") {
		t.Fatalf("Register(images/Photo.png) = %q, want assets/photo.<hash>.png", left)
	}
	if !strings.HasPrefix(right, "assets/photo.") || !strings.HasSuffix(right, ".png") {
		t.Fatalf("Register(attachments/photo.png) = %q, want assets/photo.<hash>.png", right)
	}
	if left == right {
		t.Fatalf("Register() returned %q and %q, want distinct paths for different contents", left, right)
	}

	merged := mergeAssetsForTest(vaultRoot, indexed, collector)
	if asset := merged["images/Photo.png"]; asset == nil || asset.DstPath != left {
		t.Fatalf("merged[images/Photo.png] = %#v, want stable DstPath %q", asset, left)
	}
	if asset := merged["attachments/photo.png"]; asset == nil || asset.DstPath != right {
		t.Fatalf("merged[attachments/photo.png] = %#v, want stable DstPath %q", asset, right)
	}
}

func writeAssetFile(t *testing.T, root string, relPath string, content string) {
	t.Helper()

	absPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", absPath, err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", absPath, err)
	}
}

func expectedHashedAssetPath(t *testing.T, vaultRoot string, srcPath string) string {
	t.Helper()

	hashValue, err := assetHash(vaultRoot, srcPath)
	if err != nil {
		hashValue = missingAssetHash(srcPath)
	}

	return hashedAssetPath(srcPath, hashValue)
}

func writeAssetSymlinkOrSkip(t *testing.T, targetPath string, linkPath string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(linkPath), err)
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("Symlink(%q, %q) unsupported: %v", targetPath, linkPath, err)
	}
}
