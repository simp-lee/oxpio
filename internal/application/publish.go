package application

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalfsutil "github.com/simp-lee/oxpio/internal/fsutil"
	internalpublish "github.com/simp-lee/oxpio/internal/publish"
	"github.com/simp-lee/oxpio/internal/workspace"
)

// PublishOptions controls the one shared strict publish service.
type PublishOptions struct {
	DryRun                     bool
	DiagnosticsWriter          io.Writer
	TrackOutputTransactionPath func(string)
}

type PublishResult struct {
	OutputPath string
	DryRun     bool
	Published  bool
	Build      *internalbuild.BuildResult
	Changes    []PublishChange
}

type PublishChange struct {
	Kind       string  `json:"kind"`
	Domain     string  `json:"domain"`
	Path       string  `json:"path"`
	BeforeHash *string `json:"beforeHash"`
	AfterHash  *string `json:"afterHash"`
}

// Publish runs strict analysis, artifact generation and formal output
// replacement. Dry-run uses only an OS temporary directory and no durable
// workspace state.
func Publish(vaultPath, outputPath string, options PublishOptions) (*PublishResult, error) {
	boundary, err := internalfsutil.ResolveVaultOutput(vaultPath, outputPath)
	if err != nil {
		return nil, err
	}
	result := &PublishResult{OutputPath: boundary.OutputPath, DryRun: options.DryRun}
	if options.DryRun {
		if err := workspace.Check(boundary.VaultPath); err != nil {
			return result, err
		}
		if err := internalfsutil.ValidateOutputParent(boundary); err != nil {
			return result, err
		}
		if err := internalpublish.CheckOutputBoundary(boundary.VaultPath, boundary.OutputPath); err != nil {
			return result, err
		}
		if err := internalpublish.CheckExternalOutputLock(boundary.VaultPath, boundary.OutputPath); err != nil {
			return result, err
		}
		fingerprint, err := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
		if err != nil {
			return result, err
		}
		before, err := snapshotOutput(boundary.OutputPath)
		if err != nil {
			return result, err
		}
		temporary, err := os.MkdirTemp("", "oxpio-publish-preview-")
		if err != nil {
			return result, fmt.Errorf("create publish preview: %w", err)
		}
		defer func() { _ = os.RemoveAll(temporary) }()
		result.Build, err = internalbuild.BuildWithOptions(boundary.VaultPath, temporary, internalbuild.Options{
			Strict: true, Ephemeral: true, DiagnosticsWriter: options.DiagnosticsWriter,
			SourceOutputPath: boundary.OutputPath, TrackOutputTransactionPath: options.TrackOutputTransactionPath,
		})
		if err != nil {
			return result, err
		}
		current, fingerprintErr := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
		if fingerprintErr != nil {
			return result, fingerprintErr
		}
		if current != fingerprint {
			return result, fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during publish dry-run")
		}
		after, snapshotErr := snapshotOutput(result.Build.OutputPath)
		if snapshotErr != nil {
			return result, snapshotErr
		}
		result.Changes = diffOutput(before, after)
		result.Published = false
		return result, nil
	}
	if err := internalfsutil.ValidateOutputParent(boundary); err != nil {
		return result, err
	}

	err = workspace.WithLock(boundary.VaultPath, func() error {
		before, snapshotErr := snapshotOutput(boundary.OutputPath)
		if snapshotErr != nil {
			return snapshotErr
		}
		fingerprint, fingerprintErr := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		validateInput := func() error {
			current, currentErr := workspace.Fingerprint(boundary.VaultPath, boundary.OutputPath)
			if currentErr != nil {
				return currentErr
			}
			if current != fingerprint {
				return fmt.Errorf("CONCURRENT_INPUT: Vault inputs changed during publish")
			}
			return nil
		}
		var buildErr error
		result.Build, buildErr = internalbuild.BuildWithOptions(boundary.VaultPath, boundary.OutputPath, internalbuild.Options{
			Strict: true, DiagnosticsWriter: options.DiagnosticsWriter,
			TrackOutputTransactionPath: options.TrackOutputTransactionPath,
			InputFingerprint:           fingerprint, ValidateInput: validateInput,
		})
		result.Published = buildErr == nil
		if buildErr == nil {
			after, snapshotErr := snapshotOutput(boundary.OutputPath)
			if snapshotErr != nil {
				result.Build.OutputCleanupError = snapshotErr
				return nil
			}
			result.Changes = diffOutput(before, after)
		}
		return buildErr
	})
	return result, err
}

func snapshotOutput(root string) (map[string]string, error) {
	files := make(map[string]string)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("output path is not a regular directory")
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fs.SkipDir
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		hash := sha256.Sum256(data)
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files[filepath.ToSlash(rel)] = fmt.Sprintf("%x", hash)
		return nil
	})
	return files, err
}

func diffOutput(before, after map[string]string) []PublishChange {
	paths := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		paths[path] = struct{}{}
	}
	for path := range after {
		paths[path] = struct{}{}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	changes := make([]PublishChange, 0, len(ordered))
	for _, path := range ordered {
		beforeHash, beforeOK := before[path]
		afterHash, afterOK := after[path]
		if beforeOK && afterOK && beforeHash == afterHash {
			continue
		}
		change := PublishChange{Domain: "output", Path: path}
		if beforeOK {
			change.BeforeHash = &beforeHash
		}
		if afterOK {
			change.AfterHash = &afterHash
		}
		switch {
		case !beforeOK:
			change.Kind = "create"
		case !afterOK:
			change.Kind = "delete"
		default:
			change.Kind = "update"
		}
		changes = append(changes, change)
	}
	return changes
}

// OutputDescriptor returns the public output identity without exposing a host
// path for external outputs.
func OutputDescriptor(vaultPath, outputPath string) map[string]string {
	boundary, err := workspaceOutputBoundary(vaultPath, outputPath)
	if err != nil {
		return map[string]string{"kind": "external", "path": "<external-output>"}
	}
	return map[string]string{"kind": boundary.kind, "path": boundary.path}
}

type outputDescriptor struct{ kind, path string }

func workspaceOutputBoundary(vaultPath, outputPath string) (outputDescriptor, error) {
	// Keep the descriptor adapter independent of the publisher's internal
	// transaction state while using the same canonical boundary resolver.
	resolvedVault, err := internalfsutil.ResolveVaultPath(vaultPath)
	if err != nil {
		return outputDescriptor{}, err
	}
	boundary, err := internalfsutil.ResolveVaultOutput(resolvedVault, outputPath)
	if err != nil {
		return outputDescriptor{}, err
	}
	if !internalfsutil.PathWithinRoot(resolvedVault, boundary.OutputPath) {
		return outputDescriptor{kind: "external", path: "<external-output>"}, nil
	}
	relative, err := filepath.Rel(resolvedVault, boundary.OutputPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return outputDescriptor{}, fmt.Errorf("output descriptor is external")
	}
	return outputDescriptor{kind: "vault-relative", path: filepath.ToSlash(relative)}, nil
}
