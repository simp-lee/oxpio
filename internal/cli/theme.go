package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	internalanalyze "github.com/simp-lee/oxpio/internal/analyze"
	internalbuild "github.com/simp-lee/oxpio/internal/build"
	internalconfig "github.com/simp-lee/oxpio/internal/config"
	internaldiag "github.com/simp-lee/oxpio/internal/diag"
	internalsiteplan "github.com/simp-lee/oxpio/internal/siteplan"
	internaltheme "github.com/simp-lee/oxpio/internal/theme"
	"github.com/simp-lee/oxpio/internal/workspace"
	"github.com/spf13/cobra"
)

func newThemeInitCommand() *cobra.Command {
	var vaultPath string
	var dryRun bool
	cmd := &cobra.Command{Use: "init [NAME]", Short: "Create an editable local theme baseline", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		vault, err := resolveVaultPath(vaultPath)
		if err != nil {
			return err
		}
		name := "minimal"
		if len(args) == 1 {
			name = args[0]
		}
		files, err := internaltheme.InitFiles(name)
		if err != nil {
			return err
		}
		target := filepath.Join(vault, ".oxpio", "themes", name)
		checkTarget := func() error {
			if info, statErr := os.Lstat(target); statErr == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("theme target %q must not be a symbolic link", name)
				}
				if !info.IsDir() {
					return fmt.Errorf("theme target %q is not a directory", name)
				}
				entries, readErr := os.ReadDir(target)
				if readErr != nil {
					return readErr
				}
				if len(entries) != 0 {
					return fmt.Errorf("theme target %q must be absent or empty", name)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			}
			return checkThemeParents(vault, target)
		}
		if err := checkTarget(); err != nil {
			return err
		}
		changes := themeInitChanges(name, files)
		if dryRun {
			if err := workspace.CheckDryRun(vault); err != nil {
				return err
			}
			start, fingerprintErr := workspace.Fingerprint(vault, filepath.Join(vault, "public"))
			if fingerprintErr != nil {
				return fingerprintErr
			}
			end, fingerprintErr := workspace.Fingerprint(vault, filepath.Join(vault, "public"))
			if fingerprintErr != nil {
				return fingerprintErr
			}
			if start != end {
				return errors.New("CONCURRENT_INPUT: Vault inputs changed during dry-run")
			}
			return writeThemeData(cmd, map[string]any{"theme": name, "dryRun": true, "files": sortedThemeNames(files), "changes": changes, "impactedPages": 0, "artifacts": map[string]int{"themeFiles": len(files)}, "next": "theme validate then explicitly publish"})
		}
		if err := workspace.WithLock(vault, func() error {
			if err := checkTarget(); err != nil {
				return err
			}
			return createThemeFiles(target, files)
		}); err != nil {
			return err
		}
		return writeThemeData(cmd, map[string]any{"theme": name, "created": true, "files": sortedThemeNames(files), "changes": changes, "impactedPages": 0, "artifacts": map[string]int{"themeFiles": len(files)}, "next": "theme validate then explicitly publish"})
	}}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	return cmd
}

func newThemeValidateCommand() *cobra.Command {
	var vaultPath, outputPath, themeID string
	cmd := &cobra.Command{Use: "validate", Short: "Validate a selected theme", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := resolveVaultPath(vaultPath)
		if err != nil {
			return err
		}
		if themeID != "" {
			boundary, boundaryErr := resolveVaultOutputPaths(vaultPath, outputPath)
			if boundaryErr != nil {
				return boundaryErr
			}
			cfg, cfgErr := internalconfig.LoadForThemePreview(boundary.VaultPath, boundary.OutputPath)
			if cfgErr != nil {
				return cfgErr
			}
			cfg.Theme, cfg.ThemeDir = themeID, ""
			planned, planErr := internalsiteplan.BuildForOutputWithConfig(boundary.VaultPath, boundary.OutputPath, cfg)
			if planned == nil {
				return planErr
			}
			if planErr != nil {
				return diagnosticError{err: planErr, diagnostics: diagnosticsFromBuild(planned.Diagnostics)}
			}
			if failure := internalanalyze.Failure(planned.Diagnostics); failure != nil {
				return diagnosticError{err: failure, diagnostics: diagnosticsFromBuild(planned.Diagnostics)}
			}
			var source internaltheme.Source
			if themeID == internaltheme.BaseID {
				source, err = internaltheme.BaseSource()
			} else {
				source, err = internaltheme.LoadLocal(boundary.VaultPath, themeID)
			}
			if err != nil {
				return err
			}
			warnings, errors := themeDiagnosticCounts(planned.Diagnostics)
			manifest := internaltheme.ManifestOf(source)
			return writeThemeData(cmd, map[string]any{"theme": themeID, "apiVersion": internaltheme.APIVersion, "manifest": manifest, "errors": errors, "warnings": warnings, "valid": true})
		}
		boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
		if err != nil {
			return err
		}
		cfg, err := internalconfig.LoadForBuildWithOutput(boundary.VaultPath, boundary.OutputPath)
		if err != nil {
			return err
		}
		selectedTheme := cfg.Theme
		if themeID != "" {
			selectedTheme = themeID
		}
		cfg.Theme = selectedTheme
		cfg.ThemeDir = ""
		planned, analyzeErr := internalsiteplan.BuildForOutputWithConfig(boundary.VaultPath, boundary.OutputPath, cfg)
		if planned == nil {
			return analyzeErr
		}
		if analyzeErr != nil {
			return diagnosticError{err: analyzeErr, diagnostics: diagnosticsFromBuild(planned.Diagnostics)}
		}
		if failure := internalanalyze.Failure(planned.Diagnostics); failure != nil {
			return diagnosticError{err: failure, diagnostics: diagnosticsFromBuild(planned.Diagnostics)}
		}
		var source internaltheme.Source
		if selectedTheme == internaltheme.BaseID {
			source, err = internaltheme.BaseSource()
		} else {
			source, err = internaltheme.LoadLocal(boundary.VaultPath, selectedTheme)
		}
		if err != nil {
			return err
		}
		warnings, errors := themeDiagnosticCounts(planned.Diagnostics)
		return writeThemeData(cmd, map[string]any{"theme": selectedTheme, "apiVersion": internaltheme.APIVersion, "manifest": internaltheme.ManifestOf(source), "errors": errors, "warnings": warnings, "valid": true})
	}}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&themeID, "theme", "", "Temporarily select a theme")
	return cmd
}

func themeDiagnosticCounts(items []internaldiag.Diagnostic) (warnings, errors int) {
	for _, item := range items {
		switch item.Severity {
		case internaldiag.SeverityError:
			errors++
		case internaldiag.SeverityWarning:
			warnings++
		}
	}
	return warnings, errors
}

func newThemeContextCommand() *cobra.Command {
	var vaultPath string
	cmd := &cobra.Command{Use: "context", Short: "Describe the theme View Model contract", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		vault, err := resolveVaultPath(vaultPath)
		if err != nil {
			return err
		}
		cfg, err := internalconfig.LoadForBuild(vault)
		if err != nil {
			return err
		}
		var source internaltheme.Source
		if cfg.Theme == internaltheme.BaseID {
			source, err = internaltheme.BaseSource()
		} else {
			source, err = internaltheme.LoadLocal(vault, cfg.Theme)
		}
		if err != nil {
			return err
		}
		if err := internaltheme.Validate(source); err != nil {
			return err
		}
		manifest := internaltheme.ManifestOf(source)
		for index := range manifest.Files {
			if cfg.Theme != internaltheme.BaseID {
				manifest.Files[index].Path = filepath.ToSlash(filepath.Join(".oxpio", "themes", cfg.Theme, manifest.Files[index].Path))
			}
		}
		if cfg.CustomCSS != "" {
			if data, readErr := os.ReadFile(cfg.CustomCSS); readErr == nil {
				digest := sha256.Sum256(data)
				manifest.Files = append(manifest.Files, internaltheme.File{Path: "custom.css", Kind: "text", Hash: fmt.Sprintf("%x", digest), Size: len(data)})
			}
		}
		available := make([]map[string]any, 0)
		baseSource, baseErr := internaltheme.BaseSource()
		if baseErr != nil {
			return baseErr
		}
		baseManifest := internaltheme.ManifestOf(baseSource)
		available = append(available, map[string]any{"id": internaltheme.BaseID, "apiVersion": baseManifest.APIVersion, "name": baseManifest.Name, "hash": baseManifest.Hash})
		if entries, readErr := os.ReadDir(filepath.Join(vault, ".oxpio", "themes")); readErr == nil {
			for _, entry := range entries {
				if !entry.IsDir() || entry.Name() == internaltheme.BaseID || !safeThemeSegment(entry.Name()) {
					continue
				}
				candidate, loadErr := internaltheme.LoadLocal(vault, entry.Name())
				if loadErr != nil {
					return loadErr
				}
				candidateManifest := internaltheme.ManifestOf(candidate)
				available = append(available, map[string]any{"id": entry.Name(), "apiVersion": candidateManifest.APIVersion, "name": candidateManifest.Name, "hash": candidateManifest.Hash})
			}
		}
		sort.Slice(available, func(i, j int) bool { return available[i]["id"].(string) < available[j]["id"].(string) })
		data := map[string]any{
			"apiVersion": internaltheme.APIVersion, "vm": internaltheme.VMVersion, "selectedTheme": cfg.Theme, "availableThemes": available, "theme": manifest,
			"pageKinds": []string{"section", "article", "tag", "timeline", "404"}, "layouts": []string{"base", "section", "article", "tag", "timeline", "404"},
			"fields":      []string{"Site", "Kind", "Title", "Description", "URL", "Href", "Canonical", "BasePath", "Content", "Section", "Article", "Entries", "Tags", "Backlinks", "Related", "Navigation", "Sidebar", "Breadcrumbs", "Versions", "Source", "Pagination", "SEO"},
			"functions":   []string{"siteURL(path)", "routeURL(route)", "assetURL(themePath)", "formatDate(value, layout)", "emitJSONLD(value)"},
			"assetHelper": "assetURL accepts only a planned theme-relative assets/<path> resource",
			"constraints": map[string]string{"source": "Markdown is the only content source; Content is controlled template.HTML", "urls": "siteURL and routeURL accept normalized relative paths", "seo": "SEO and canonical values are core-provided", "preview": "theme validate and theme preview do not write source, config, or formal output"},
			"example":     map[string]any{"kind": "article", "title": "Example", "content": "{{.Content}}", "asset": "{{assetURL \"assets/style.css\"}}"},
			"boundaries":  map[string]string{"source": "Markdown only", "theme": ".oxpio/themes/<theme-id>", "output": "publish only"},
		}
		return writeThemeData(cmd, data)
	}}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	return cmd
}

func newThemeReadCommand() *cobra.Command {
	var vaultPath string
	var raw bool
	cmd := &cobra.Command{Use: "read PATH", Short: "Read a local theme file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		vault, err := resolveVaultPath(vaultPath)
		if err != nil {
			return err
		}
		candidate, id, err := resolveThemeFile(vault, args[0])
		if err != nil {
			return err
		}
		if _, kindErr := internaltheme.FileKind(args[0]); kindErr != nil {
			return kindErr
		}
		data, err := readThemeFile(vault, candidate)
		if err != nil {
			return err
		}
		if raw && formatOf(cmd) == "text" {
			_, err = cmd.OutOrStdout().Write(data)
			return err
		}
		digest := sha256.Sum256(data)
		result := map[string]any{"path": args[0], "theme": id, "content": string(data), "sha256": fmt.Sprintf("%x", digest), "size": len(data), "kind": themeFileKind(args[0])}
		if themeFileKind(args[0]) == "binary" {
			result["content"] = nil
		}
		return writeThemeData(cmd, result)
	}}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().BoolVar(&raw, "raw", false, "Write binary bytes in text mode")
	return cmd
}

func newThemeWriteCommand() *cobra.Command {
	var vaultPath, contentFile, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "write PATH", Short: "Replace a local theme file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		vault, err := resolveVaultPath(vaultPath)
		if err != nil {
			return err
		}
		candidate, _, err := resolveThemeFile(vault, args[0])
		if err != nil {
			return err
		}
		kind, kindErr := internaltheme.FileKind(args[0])
		if kindErr != nil {
			return kindErr
		}
		data, err := readThemeCLIContent(cmd, vault, contentFile)
		if err != nil {
			return err
		}
		if kind == "text" && !utf8.Valid(data) {
			return fmt.Errorf("theme text file must be valid UTF-8")
		}
		if err := internaltheme.ValidateFileData(args[0], data); err != nil {
			return err
		}
		return mutateThemeFile(cmd, vault, candidate, args[0], data, expected, dryRun)
	}}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "Complete file content or -")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash or absent")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("content-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newThemeEditCommand() *cobra.Command {
	var vaultPath, editsFile, expected string
	var dryRun bool
	cmd := &cobra.Command{Use: "edit PATH", Short: "Apply exact edits to a local theme text file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		vault, err := resolveVaultPath(vaultPath)
		if err != nil {
			return err
		}
		candidate, _, err := resolveThemeFile(vault, args[0])
		if err != nil {
			return err
		}
		kind, kindErr := internaltheme.FileKind(args[0])
		if kindErr != nil {
			return kindErr
		}
		if kind == "binary" {
			return fmt.Errorf("theme edit requires a text file")
		}
		patch, err := readThemeCLIContent(cmd, vault, editsFile)
		if err != nil {
			return err
		}
		if dryRun {
			old, err := readThemeFile(vault, candidate)
			if err != nil {
				return err
			}
			next, err := applyThemeEdits(old, patch)
			if err != nil {
				return err
			}
			return mutateThemeFile(cmd, vault, candidate, args[0], next, expected, true)
		}
		return workspace.WithLock(vault, func() error {
			old, err := readThemeFile(vault, candidate)
			if err != nil {
				return err
			}
			next, err := applyThemeEdits(old, patch)
			if err != nil {
				return err
			}
			return mutateThemeFileLocked(cmd, vault, candidate, args[0], next, expected, false)
		})
	}}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&editsFile, "edits-file", "", "JSON patch file or -")
	cmd.Flags().StringVar(&expected, "if-hash", "", "SHA-256 hash")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Do not write")
	_ = cmd.MarkFlagRequired("edits-file")
	_ = cmd.MarkFlagRequired("if-hash")
	return cmd
}

func newThemePreviewCommand(deps commandDependencies) *cobra.Command {
	var vaultPath, outputPath, themeID string
	var port int
	cmd := &cobra.Command{Use: "preview", Short: "Run an isolated local theme preview", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		boundary, err := resolveVaultOutputPaths(vaultPath, outputPath)
		if err != nil {
			return err
		}
		if port < 1 || port > 65535 {
			return fmt.Errorf("theme preview port must be between 1 and 65535")
		}
		var previewSource internaltheme.Source
		var previewBaseURL string
		var previewAssets map[string]string
		if themeID != "" {
			if themeID == internaltheme.BaseID {
				previewSource, err = internaltheme.BaseSource()
			} else {
				previewSource, err = internaltheme.LoadLocal(boundary.VaultPath, themeID)
			}
			if err == nil {
				err = internalsiteplan.ValidateThemeSource(boundary.VaultPath, previewSource)
			}
			if err != nil {
				return err
			}
		}
		temporary, err := os.MkdirTemp("", "oxpio-theme-preview-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(temporary) }()
		var previewBuild *internalbuild.BuildResult
		if themeID != "" {
			cfg, cfgErr := internalconfig.LoadForThemePreview(boundary.VaultPath, boundary.OutputPath)
			if cfgErr != nil {
				return cfgErr
			}
			cfg.Theme, cfg.ThemeDir = themeID, ""
			cfg.BaseURL, err = previewBaseURLForPort(cfg.BaseURL, port)
			if err != nil {
				return err
			}
			previewBaseURL = cfg.BaseURL
			planned, planErr := internalsiteplan.BuildForOutputWithConfig(boundary.VaultPath, boundary.OutputPath, cfg)
			if planErr != nil {
				return planErr
			}
			previewAssets = planned.Plan.ThemeAssetURLs
			previewBuild, err = internalbuild.BuildWithConfig(boundary.VaultPath, temporary, cfg, internalbuild.Options{Strict: true, Ephemeral: true, SourceOutputPath: boundary.OutputPath})
		} else {
			cfg, cfgErr := internalconfig.LoadForBuildWithOutput(boundary.VaultPath, boundary.OutputPath)
			if cfgErr != nil {
				return cfgErr
			}
			cfg.BaseURL, err = previewBaseURLForPort(cfg.BaseURL, port)
			if err != nil {
				return err
			}
			previewBaseURL = cfg.BaseURL
			planned, planErr := internalsiteplan.BuildForOutputWithConfig(boundary.VaultPath, boundary.OutputPath, cfg)
			if planErr != nil {
				return planErr
			}
			previewAssets = planned.Plan.ThemeAssetURLs
			if cfg.Theme == internaltheme.BaseID {
				previewSource, err = internaltheme.BaseSource()
			} else {
				previewSource, err = internaltheme.LoadLocal(boundary.VaultPath, cfg.Theme)
			}
			if err == nil {
				previewBuild, err = internalbuild.BuildWithConfig(boundary.VaultPath, temporary, cfg, internalbuild.Options{Strict: true, Ephemeral: true, SourceOutputPath: boundary.OutputPath})
			}
		}
		if err != nil {
			return err
		}
		if previewBuild == nil {
			return fmt.Errorf("theme preview produced no result")
		}
		if err := writeThemeSamplePages(temporary, previewSource, previewAssets, previewBaseURL); err != nil {
			return err
		}
		server, err := deps.newPreviewServer(temporary, port)
		if err != nil {
			return err
		}
		defer func() { _ = closePreviewServer(server) }()
		announce := func() error {
			return writeThemeData(cmd, map[string]any{"preview": true, "address": fmt.Sprintf("127.0.0.1:%d", port), "port": port})
		}
		if ready, ok := server.(interface {
			ListenAndServeWithReadyHandlerOn(string, http.Handler, func() error) error
		}); ok {
			handler, handlerOK := server.(http.Handler)
			if !handlerOK {
				return fmt.Errorf("theme preview server is not an HTTP handler")
			}
			previewHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if serveThemeSample(response, request, filepath.Join(temporary, "_oxpio", "theme", "sample")) {
					return
				}
				handler.ServeHTTP(response, request)
			})
			return ready.ListenAndServeWithReadyHandlerOn("127.0.0.1", previewHandler, announce)
		}
		if err := announce(); err != nil {
			return err
		}
		return server.ListenAndServe()
	}}
	cmd.Flags().StringVar(&vaultPath, "vault", "", "Path to the Vault")
	cmd.Flags().StringVar(&outputPath, "output", "", "Formal output boundary")
	cmd.Flags().StringVar(&themeID, "theme", "", "Temporarily select a theme")
	cmd.Flags().IntVar(&port, "port", 8080, "Listener port")
	return cmd
}

func previewBaseURLForPort(base string, port int) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("theme preview base URL is invalid")
	}
	parsed.Scheme = "http"
	parsed.Host = fmt.Sprintf("127.0.0.1:%d", port)
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func serveThemeSample(response http.ResponseWriter, request *http.Request, root string) bool {
	prefix := "/_oxpio/theme/sample/"
	if request.Method != http.MethodGet || !strings.HasPrefix(request.URL.Path, prefix) {
		return false
	}
	rel := strings.TrimPrefix(request.URL.Path, prefix)
	if rel == "" || strings.Contains(rel, "\\") || strings.Contains(rel, "..") {
		http.NotFound(response, request)
		return true
	}
	candidate := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(candidate)
	if err != nil {
		http.NotFound(response, request)
		return true
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = response.Write(data)
	return true
}

func writeThemeSamplePages(root string, source internaltheme.Source, assets map[string]string, baseURL string) error {
	if len(source.Files) == 0 {
		return nil
	}
	renderer, err := internaltheme.Compile(source, assets, baseURL)
	if err != nil {
		return err
	}
	for _, kind := range []string{"section", "article", "tag", "timeline", "404"} {
		data, renderErr := renderer.Render(kind, internaltheme.SamplePage(kind))
		if renderErr != nil {
			return renderErr
		}
		directory := filepath.Join(root, "_oxpio", "theme", "sample", kind)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, "index.html"), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeThemeData(cmd *cobra.Command, value any) error {
	if formatOf(cmd) == "json" {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(append(data, '\n'))
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return err
}

func sortedThemeNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func themeInitChanges(themeID string, files map[string][]byte) []any {
	changes := make([]any, 0, len(files))
	for _, name := range sortedThemeNames(files) {
		digest := sha256.Sum256(files[name])
		changes = append(changes, map[string]any{"kind": "create", "domain": "theme", "path": filepath.ToSlash(filepath.Join(".oxpio", "themes", themeID, name)), "beforeHash": nil, "afterHash": fmt.Sprintf("%x", digest)})
	}
	return changes
}

func createThemeFiles(root string, files map[string][]byte) error {
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(root), ".oxpio-theme-stage-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	for _, name := range sortedThemeNames(files) {
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := workspace.AtomicWriteFile(target, files[name], 0o644); err != nil {
			return err
		}
	}
	backup := ""
	if info, statErr := os.Lstat(root); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("theme target must be a regular directory")
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("theme target must be absent or empty")
		}
		backup, err = os.MkdirTemp(filepath.Dir(root), ".oxpio-theme-backup-*")
		if err != nil {
			return err
		}
		if err := os.Remove(backup); err != nil {
			return err
		}
		if err := os.Rename(root, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, root); err != nil {
		if backup != "" {
			_ = os.Rename(backup, root)
		}
		return err
	}
	if backup != "" {
		if err := os.Remove(backup); err != nil {
			return fmt.Errorf("remove empty theme backup: %w", err)
		}
	}
	return nil
}

func resolveThemeFile(vault, value string) (string, string, error) {
	rel := strings.TrimSpace(value)
	if strings.Contains(rel, "\\") {
		return "", "", fmt.Errorf("theme path must use POSIX separators")
	}
	if rel == "custom.css" {
		return filepath.Join(vault, rel), "custom", nil
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 4 || parts[0] != ".oxpio" || parts[1] != "themes" || !safeThemeSegment(parts[2]) || parts[2] == internaltheme.BaseID || !safeThemeRelative(parts[3:]) {
		return "", "", fmt.Errorf("theme path must be inside .oxpio/themes/<theme-id>")
	}
	candidate := filepath.Join(vault, filepath.FromSlash(rel))
	return candidate, parts[2], nil
}
func safeThemeSegment(value string) bool { return internaltheme.ValidID(value) }
func safeThemeRelative(parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	return internaltheme.ValidRelativePath(strings.Join(parts, "/"))
}

func readThemeFile(vault, candidate string) ([]byte, error) {
	if err := checkThemeParents(vault, candidate); err != nil {
		return nil, err
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("theme target must be a regular file")
	}
	return os.ReadFile(candidate)
}

func checkThemeParents(vault, candidate string) error {
	rel, err := filepath.Rel(vault, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("theme path escapes the Vault")
	}
	current := vault
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for index, part := range parts {
		current = filepath.Join(current, filepath.FromSlash(part))
		if index == len(parts)-1 {
			break
		}
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("theme parent must be a regular directory")
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	return nil
}

func themeFileKind(name string) string {
	kind, err := internaltheme.FileKind(name)
	if err != nil {
		return ""
	}
	return kind
}

func readThemeCLIContent(cmd *cobra.Command, vault, name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	if name == "" {
		return nil, fmt.Errorf("content input is required")
	}
	if strings.HasPrefix(name, ".oxpio/") || name == ".oxpio" || strings.HasPrefix(name, ".obsidian/") || name == ".obsidian" {
		return nil, fmt.Errorf("theme input is outside the theme source boundary")
	}
	if !internaltheme.ValidRelativePath(name) {
		return nil, fmt.Errorf("input path %q must be a Vault-relative path", name)
	}
	return readThemeFile(vault, filepath.Join(vault, filepath.FromSlash(name)))
}

func mutateThemeFile(cmd *cobra.Command, vault, candidate, rel string, data []byte, expected string, dryRun bool) error {
	if !dryRun {
		return workspace.WithLock(vault, func() error {
			return mutateThemeFileLocked(cmd, vault, candidate, rel, data, expected, false)
		})
	}
	if err := workspace.CheckDryRun(vault); err != nil {
		return err
	}
	return mutateThemeFileLocked(cmd, vault, candidate, rel, data, expected, true)
}

func mutateThemeFileLocked(cmd *cobra.Command, vault, candidate, rel string, data []byte, expected string, dryRun bool) error {
	var startFingerprint string
	if dryRun {
		var err error
		startFingerprint, err = workspace.Fingerprint(vault, filepath.Join(vault, "public"))
		if err != nil {
			return err
		}
	}
	if err := internaltheme.ValidateFileData(rel, data); err != nil {
		return err
	}
	if len(data) > 16*1024*1024 {
		return fmt.Errorf("theme file exceeds 16 MiB")
	}
	if err := checkThemeParents(vault, candidate); err != nil {
		return err
	}
	old, err := readThemeFile(vault, candidate)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if expected == "" {
		return fmt.Errorf("if-hash is required")
	}
	before := "absent"
	if exists {
		digest := sha256.Sum256(old)
		before = fmt.Sprintf("%x", digest)
	}
	if expected != before {
		return fmt.Errorf("CONFLICT: theme file hash mismatch")
	}
	latest, latestErr := readThemeFile(vault, candidate)
	latestHash := "absent"
	if latestErr == nil {
		latestDigest := sha256.Sum256(latest)
		latestHash = fmt.Sprintf("%x", latestDigest)
	} else if !errors.Is(latestErr, os.ErrNotExist) {
		return latestErr
	}
	if latestHash != before {
		return fmt.Errorf("CONCURRENT_INPUT: theme file changed during mutation")
	}
	digest := sha256.Sum256(data)
	change := map[string]any{"kind": "update", "domain": "theme", "path": rel, "beforeHash": nil, "afterHash": fmt.Sprintf("%x", digest)}
	if !exists {
		change["kind"] = "create"
	} else {
		change["beforeHash"] = before
	}
	if dryRun {
		end, fingerprintErr := workspace.Fingerprint(vault, filepath.Join(vault, "public"))
		if fingerprintErr != nil {
			return fingerprintErr
		}
		if end != startFingerprint {
			return errors.New("CONCURRENT_INPUT: Vault inputs changed during dry-run")
		}
		return writeThemeData(cmd, map[string]any{"dryRun": true, "changes": []any{change}, "impactedPages": 0, "artifacts": map[string]int{"themeFiles": 1}, "next": "theme validate then explicitly publish"})
	}
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		return err
	}
	var writeErr error
	if exists {
		writeErr = workspace.AtomicWriteFile(candidate, data, 0o644)
	} else {
		writeErr = workspace.AtomicCreateFile(candidate, data, 0o644)
	}
	if writeErr != nil {
		if !exists && errors.Is(writeErr, os.ErrExist) {
			return fmt.Errorf("CONFLICT: theme file appeared during mutation")
		}
		return writeErr
	}
	return writeThemeData(cmd, map[string]any{"changes": []any{change}, "path": rel, "impactedPages": 0, "artifacts": map[string]int{"themeFiles": 1}, "next": "theme validate then explicitly publish"})
}

type themeEdit struct {
	OldText *string `json:"oldText"`
	NewText *string `json:"newText"`
}
type themeEditRequest struct {
	Edits []themeEdit `json:"edits"`
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch delimiter := token.(type) {
		case json.Delim:
			switch delimiter {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, keyErr := decoder.Token()
					if keyErr != nil {
						return keyErr
					}
					name, ok := key.(string)
					if !ok {
						return fmt.Errorf("JSON object key must be a string")
					}
					if seen[name] {
						return fmt.Errorf("duplicate JSON key %q", name)
					}
					seen[name] = true
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			case '[':
				for decoder.More() {
					if err := walk(); err != nil {
						return err
					}
				}
				_, err = decoder.Token()
				return err
			}
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("edits input must contain one JSON object")
	}
	return nil
}

func applyThemeEdits(source, data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("edits input must be valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var request themeEditRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("edits input must contain one JSON object")
	}
	if len(request.Edits) == 0 {
		return nil, fmt.Errorf("edits must not be empty")
	}
	type located struct {
		start, end  int
		replacement []byte
	}
	locatedEdits := make([]located, 0, len(request.Edits))
	for _, edit := range request.Edits {
		if edit.OldText == nil || edit.NewText == nil {
			return nil, fmt.Errorf("oldText and newText must be strings")
		}
		if *edit.OldText == "" {
			return nil, fmt.Errorf("oldText must not be empty")
		}
		old := []byte(*edit.OldText)
		start := bytes.Index(source, old)
		if start < 0 || bytes.Contains(source[start+1:], old) {
			return nil, fmt.Errorf("oldText must match exactly once")
		}
		locatedEdits = append(locatedEdits, located{start: start, end: start + len(old), replacement: []byte(*edit.NewText)})
	}
	sort.Slice(locatedEdits, func(i, j int) bool { return locatedEdits[i].start < locatedEdits[j].start })
	for index := 1; index < len(locatedEdits); index++ {
		if locatedEdits[index].start < locatedEdits[index-1].end {
			return nil, fmt.Errorf("edits must not overlap")
		}
	}
	result := make([]byte, 0, len(source))
	cursor := 0
	for _, edit := range locatedEdits {
		result = append(result, source[cursor:edit.start]...)
		result = append(result, edit.replacement...)
		cursor = edit.end
	}
	result = append(result, source[cursor:]...)
	return result, nil
}
