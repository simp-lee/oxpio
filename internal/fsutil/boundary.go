package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathOutsideRoot indicates that a configured input does not belong to its vault.
var ErrPathOutsideRoot = errors.New("path must stay inside the vault")

// ErrSymlinkPath indicates that an input path contains a symbolic-link component.
var ErrSymlinkPath = errors.New("path must not contain symbolic links")

// VaultOutputBoundary is the canonical filesystem identity used by scan, build, and watch.
type VaultOutputBoundary struct {
	VaultPath  string
	OutputPath string
}

// ResolveVaultPath resolves the filesystem identity of an existing vault directory.
func ResolveVaultPath(vaultPath string) (string, error) {
	trimmed := strings.TrimSpace(vaultPath)
	if trimmed == "" {
		return "", errors.New("vault path is required")
	}

	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("resolve vault path %q: %w", trimmed, err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve vault path %q: %w", absolute, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve vault path %q: %w", absolute, err)
	}
	resolved = filepath.Clean(resolved)

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat vault path %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("vault path %q is not a directory", resolved)
	}
	return resolved, nil
}

// ResolveVaultOutput resolves a vault and every existing output ancestor once,
// rejects a symlink final output, and validates their safe relationship.
func ResolveVaultOutput(vaultPath string, outputPath string) (VaultOutputBoundary, error) {
	vaultRoot, err := ResolveVaultPath(vaultPath)
	if err != nil {
		return VaultOutputBoundary{}, err
	}

	trimmedOutput := strings.TrimSpace(outputPath)
	if trimmedOutput == "" {
		return VaultOutputBoundary{}, errors.New("output path is required")
	}
	absoluteOutput, err := filepath.Abs(trimmedOutput)
	if err != nil {
		return VaultOutputBoundary{}, fmt.Errorf("resolve output path %q: %w", trimmedOutput, err)
	}
	absoluteOutput = filepath.Clean(absoluteOutput)

	if info, lstatErr := os.Lstat(absoluteOutput); lstatErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return VaultOutputBoundary{}, fmt.Errorf("output path %q must not be a symbolic link", absoluteOutput)
		}
		if !info.IsDir() {
			return VaultOutputBoundary{}, fmt.Errorf("output path %q must be a directory", absoluteOutput)
		}
	} else if !errors.Is(lstatErr, os.ErrNotExist) {
		return VaultOutputBoundary{}, fmt.Errorf("stat output path %q: %w", absoluteOutput, lstatErr)
	}

	resolvedOutput, err := resolveThroughExistingAncestors(absoluteOutput)
	if err != nil {
		return VaultOutputBoundary{}, fmt.Errorf("resolve output path %q: %w", absoluteOutput, err)
	}

	if rebased, ok := rebasePhysicalDescendant(vaultRoot, resolvedOutput); ok {
		resolvedOutput = rebased
	}

	if SamePath(vaultRoot, resolvedOutput) {
		return VaultOutputBoundary{}, fmt.Errorf("output path %q must not equal the vault root %q", absoluteOutput, vaultRoot)
	}
	if PathWithinRoot(resolvedOutput, vaultRoot) {
		return VaultOutputBoundary{}, fmt.Errorf("output path %q must not contain the vault %q", absoluteOutput, vaultRoot)
	}

	return VaultOutputBoundary{
		VaultPath:  vaultRoot,
		OutputPath: resolvedOutput,
	}, nil
}

// PathWithinRoot reports whether candidate is root or one of its descendants.
func PathWithinRoot(root string, candidate string) bool {
	cleanRoot := filepath.Clean(root)
	cleanCandidate := filepath.Clean(candidate)
	relative, err := filepath.Rel(cleanRoot, cleanCandidate)
	if err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return true
	}

	rootInfo, err := os.Stat(cleanRoot)
	if err != nil {
		return false
	}
	current := cleanCandidate
	for {
		candidateInfo, statErr := os.Lstat(current)
		if statErr != nil {
			return false
		}
		if candidateInfo.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if os.SameFile(rootInfo, candidateInfo) {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

// PathWithinRootAfterSymlinks reports whether candidate resolves to root or one
// of its descendants, including when candidate's final components do not yet
// exist. It is intended for virtual paths such as immutable build overlays.
func PathWithinRootAfterSymlinks(root string, candidate string) bool {
	resolvedRoot, err := ResolvePathAfterSymlinks(root)
	if err != nil {
		return false
	}
	resolvedCandidate, err := ResolvePathAfterSymlinks(candidate)
	if err != nil {
		return false
	}
	return PathWithinRoot(resolvedRoot, resolvedCandidate)
}

// ResolvePathAfterSymlinks resolves existing path components while preserving
// missing final components. It is useful for validating virtual paths before a
// build creates them.
func ResolvePathAfterSymlinks(value string) (string, error) {
	return resolvePathWithMissingLeaf(value)
}

func resolvePathWithMissingLeaf(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	missingSuffix := make([]string, 0, 4)
	for attempts := 0; attempts < 256; attempts++ {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr == nil {
				resolved, err = filepath.Abs(resolved)
				if err != nil {
					return "", err
				}
				for index := len(missingSuffix) - 1; index >= 0; index-- {
					resolved = filepath.Join(resolved, missingSuffix[index])
				}
				return filepath.Clean(resolved), nil
			}
			if info.Mode()&os.ModeSymlink == 0 {
				return "", resolveErr
			}
			target, readErr := os.Readlink(current)
			if readErr != nil {
				return "", readErr
			}
			if target == "" {
				return "", errors.New("symbolic link target is empty")
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			current = filepath.Clean(target)
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", statErr
		}
		missingSuffix = append(missingSuffix, filepath.Base(current))
		current = parent
	}
	return "", errors.New("too many symbolic-link components")
}

// SamePath compares lexical paths and, when both paths exist, their filesystem identity.
func SamePath(left string, right string) bool {
	cleanLeft := filepath.Clean(left)
	cleanRight := filepath.Clean(right)
	if cleanLeft == cleanRight {
		return true
	}

	leftInfo, leftErr := os.Stat(cleanLeft)
	rightInfo, rightErr := os.Stat(cleanRight)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func rebasePhysicalDescendant(root string, candidate string) (string, bool) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return candidate, false
	}

	current := filepath.Clean(candidate)
	suffix := make([]string, 0, 4)
	for {
		currentInfo, statErr := os.Stat(current)
		if statErr == nil {
			if os.SameFile(rootInfo, currentInfo) {
				elements := make([]string, 1, len(suffix)+1)
				elements[0] = root
				for index := len(suffix) - 1; index >= 0; index-- {
					elements = append(elements, suffix[index])
				}
				return filepath.Join(elements...), true
			}
			parent := filepath.Dir(current)
			if parent == current {
				return candidate, false
			}
			suffix = append(suffix, filepath.Base(current))
			current = parent
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return candidate, false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return candidate, false
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func resolveThroughExistingAncestors(candidate string) (string, error) {
	current := filepath.Clean(candidate)
	pending := make([]string, 0, 4)

	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", fmt.Errorf("resolve path %q: %w", current, err)
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", fmt.Errorf("resolve path %q: %w", current, err)
			}
			for index := len(pending) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, pending[index])
			}
			return filepath.Clean(resolved), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat path %q: %w", current, err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("reached missing filesystem root from %q", candidate)
		}
		pending = append(pending, filepath.Base(current))
		current = parent
	}
}

// InspectContainedRegularFile validates every component from vaultRoot to
// candidate and returns an absolute regular-file path with no symlink component.
func InspectContainedRegularFile(vaultRoot string, candidate string) (string, os.FileInfo, error) {
	return inspectContainedPath(vaultRoot, candidate, false)
}

// InspectContainedDirectory validates every component from vaultRoot to
// candidate and returns an absolute directory path with no symlink component.
func InspectContainedDirectory(vaultRoot string, candidate string) (string, os.FileInfo, error) {
	return inspectContainedPath(vaultRoot, candidate, true)
}

func inspectContainedPath(vaultRoot string, candidate string, wantDirectory bool) (string, os.FileInfo, error) {
	root := strings.TrimSpace(vaultRoot)
	if root == "" {
		return "", nil, errors.New("vault path is required")
	}
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return "", nil, fmt.Errorf("resolve vault path %q: %w", vaultRoot, err)
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", nil, fmt.Errorf("stat vault path %q: %w", root, err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", nil, fmt.Errorf("vault path %q must be a resolved directory", root)
	}

	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return "", nil, os.ErrNotExist
	}

	absolute := trimmed
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, absolute)
	}
	absolute, err = filepath.Abs(absolute)
	if err != nil {
		return "", nil, err
	}
	absolute = filepath.Clean(absolute)

	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("%w: %q", ErrPathOutsideRoot, absolute)
	}

	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("%w: %q", ErrSymlinkPath, current)
		}

		last := index == len(parts)-1
		if !last {
			if !info.IsDir() {
				return "", nil, ErrUnsupportedRegularFileSource
			}
			continue
		}
		if wantDirectory {
			if !info.IsDir() {
				return "", nil, ErrUnsupportedRegularFileSource
			}
		} else if !info.Mode().IsRegular() {
			return "", nil, ErrUnsupportedRegularFileSource
		}
		return current, info, nil
	}

	return "", nil, os.ErrNotExist
}

// OpenContainedRegularFile opens a validated vault file and verifies that the
// opened object is the same file that was inspected. The caller must close it.
func OpenContainedRegularFile(vaultRoot string, candidate string) (string, *os.File, os.FileInfo, error) {
	resolvedPath, inspected, err := InspectContainedRegularFile(vaultRoot, candidate)
	if err != nil {
		return "", nil, nil, err
	}

	file, err := os.Open(resolvedPath)
	if err != nil {
		return "", nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		return "", nil, nil, errors.Join(err, file.Close())
	}
	if !os.SameFile(inspected, opened) || !opened.Mode().IsRegular() {
		return "", nil, nil, errors.Join(ErrUnsupportedRegularFileSource, file.Close())
	}
	return resolvedPath, file, opened, nil
}

// ReadContainedRegularFile reads a validated vault file.
func ReadContainedRegularFile(vaultRoot string, candidate string) (resolvedPath string, data []byte, info os.FileInfo, err error) {
	resolvedPath, file, info, err := OpenContainedRegularFile(vaultRoot, candidate)
	if err != nil {
		return "", nil, nil, err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()

	data, err = io.ReadAll(file)
	if err != nil {
		return "", nil, nil, err
	}
	return resolvedPath, data, info, nil
}

// IsPortableSitePath reports whether every slash-separated output component is
// usable as both a relative URL path and a regular filesystem name on supported platforms.
func IsPortableSitePath(value string) bool {
	// Raw fragment markers and percent escapes do not identify the same static
	// resource in a browser as they do on disk, so they are not portable here.
	for component := range strings.SplitSeq(value, "/") {
		if component == "" || strings.ContainsAny(component, `<>:"\|?*#%`) || strings.TrimRight(component, " .") != component {
			return false
		}
		for _, character := range component {
			if character < 0x20 || character == 0x7f {
				return false
			}
		}

		base := component
		if dot := strings.IndexByte(base, '.'); dot >= 0 {
			base = base[:dot]
		}
		upperBase := strings.ToUpper(strings.TrimRight(base, " "))
		switch upperBase {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return false
		}
		characters := []rune(upperBase)
		if len(characters) == 4 && (string(characters[:3]) == "COM" || string(characters[:3]) == "LPT") {
			switch characters[3] {
			case '¹', '²', '³':
				return false
			}
		}
	}
	return true
}
