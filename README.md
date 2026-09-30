# Obsite

```bash
cd vault && obsite init && obsite build && obsite serve --watch
```

Obsite is a self-contained Go CLI that publishes one Obsidian vault as a deterministic static site. The vault is the only content source and `<vault>/obsite.yaml` is the only site configuration. The generated site is written transactionally to `<vault>/public` by default.

## Commands

```text
obsite init [--vault PATH]
obsite build [--vault PATH] [--output PATH] [--strict]
obsite validate [--vault PATH] [--output PATH]
obsite serve [--vault PATH] [--output PATH] [--port NUM] [--watch]
obsite edit [--vault PATH] [--output PATH] [--port NUM] [--setup]
obsite version
obsite --version
```

`init` accepts only a nonexistent or completely empty directory. It creates a strict `obsite.yaml` and a root `_index.md`, and never overwrites existing files. `validate` is read-only and returns a failure for either errors or warnings. It resolves `--output` exactly like `build` (default `<vault>/public`); pass the same value when validating a custom-output build. Only that resolved path is excluded as the formal output boundary. Obsite does not infer additional output boundaries from directory names or existing `.obsite-output` markers; the normal fixed vault-input exclusions still apply. Normal builds show warnings and continue; `build --strict` fails before publication for either severity. `serve --watch` uses the same strict build and reloads only after a successful rebuild. `serve` never registers editor or login routes.

### Optional edit mode

Enable the single-account editor explicitly with:

```bash
obsite edit --vault ./vault --port 8080
```

The command builds before listening and serves the generated site anonymously. Configure an account interactively once with `obsite edit --setup --vault ./vault`; setup requires a terminal, refuses an existing or invalid account, writes only an Argon2id PHC hash atomically, and never prints the password. The strict `edit` section contains only `username` and `passwordHash`:

```yaml
edit:
  username: admin
  passwordHash: $argon2id$v=19$m=65536,t=3,p=1$...
```

After login, mapped article and section pages show an in-memory edit link. The embedded editor uses a locally bundled CodeMirror 6 Markdown editor with Markdown shortcuts, a toolbar, a frontmatter form that hides YAML by default, and a source mode for advanced edits. It supports server-rendered draft previews, an authenticated image upload/media picker, and local assets without CDN requests. It preserves the submitted source bytes, validates and rebuilds before an atomic source/output transaction, and reports warnings or build failures. New articles are drafts by default (`title`, `publish: false`, `type: doc`); posts require an explicit date. The File Manager supports creating folders and Markdown files, renaming managed files or folders, and confirmed recursive deletion, with path validation and SHA-256 CAS checks. Failures do not reload the public site. `serve`, including `serve --watch`, remains read-only.

## Vault and section model

A publishable vault must contain a root `_index.md`. Every content directory must contain exactly one `_index.md`; a section source is never an article. Section frontmatter allows only:

```yaml
---
title: Documentation
publish: true
description: Public documentation.
order: 1
banner: images/docs-banner.png
bannerAlt: Documentation banner
---
```

`title` and `publish` are required. Section routes are derived from their vault-relative directory (`/` for the root and `/guide/` for `guide`). A published section produces a landing page with its own body, direct child sections, direct published articles, breadcrumbs, navigation, and optional banner. `publish: false` hides the section and all descendants and is diagnosed when a descendant attempts to publish.

Ordinary articles also require explicit `title`, `publish`, and `type`. `type` is one of `doc`, `post`, or `page`:

```yaml
---
title: Getting Started
publish: true
type: doc
order: 1
description: A short description.
tags: [guide]
author: Documentation Team
status: stable
---
```

Document ordering uses explicit `order`, filename numeric prefix, normalized title, and normalized source path. A complete tie on the prescribed keys is rejected rather than resolved by an implementation-specific fallback; the same rule applies to post and page lists. Document pages expose one collection's previous/next flow and position. Posts require an explicit date, are ordered by date, and are the only article type included in RSS. Pages are standalone. No title, `publish`, date, or folder page is inferred from filenames or filesystem time. By default, an article route appends a segment derived from the source basename: `.md` and one leading decimal numeric prefix together with its following `-`, `_`, `.`, or space separator are removed. Renaming the source file therefore changes its public URL unless the article defines an explicit `slug`, which replaces the basename-derived segment.

## Configuration

YAML keys, values, nulls, and nested structures are strict. Unknown keys, duplicate keys, removed legacy fields, invalid paths, and invalid values are errors. `navigation` is required even when empty:

```yaml
baseURL: https://example.com/docs/
title: My Obsite Site
author: Site Team
description: Project documentation.
language: en
navigation:
  - name: Home
    section: .
  - name: Repository
    url: https://github.com/example/project
source:
  editURL: https://github.com/example/project/edit/main/:path
  viewURL: https://github.com/example/project/blob/main/:path
versions:
  root: docs
  default: v2
  entries:
    - id: v1
      label: Version 1
      source: v1
    - id: v2
      label: Version 2
      source: v2
sidebar:
  enabled: true
popover:
  enabled: true
related:
  enabled: true
  count: 5
rss:
  enabled: true
timeline:
  enabled: false
  path: timeline
defaultImg: ""
```

Navigation entries use exactly one site-relative `url` or section reference. Site-relative links honor `baseURL`; external HTTP(S) links are rendered as supplied and are never requested. Source `editURL` and `viewURL` are independent absolute templates with exactly one `:path`; each source path segment is RFC 3986 percent-encoded.

Explicit `versions` create independent section trees, sidebars, routes, canonical URLs, sitemaps, breadcrumbs, reading flows, source paths, and static version selectors. Cross-version identity is source-based: a section uses its directory path relative to the entry's `source` (the version root is the empty relative path), and an article uses its Markdown source path relative to that directory, including its filename and `.md` extension. After separators are represented as `/`, the complete relative path is Unicode NFKC-normalized and case-folded. Slugs, titles, generated routes, and numeric-prefix removal do not participate. Therefore matching `intro.md` sources stay linked when their slugs differ; if no published page of the same kind has the same identity key, the selector links to the target version root and never falls back to another version's content.

### `defaultImg`

`defaultImg` has only two non-empty meanings:

1. An absolute HTTP(S) URL with a host is an explicit hosted image URL.
2. A normalized vault-relative path is a local planned asset.

Empty means no default image. Local paths must be contained regular files and cannot contain query/fragment syntax, backslashes, traversal, or symlinks. Obsite never downloads an image.

## Rendering and metadata

The fixed HTML shell is server-rendered and usable without JavaScript. It includes global navigation, section/version context, breadcrumbs, Sidebar data, source links, semantic landmarks, canonical URLs, descriptions, Open Graph/Twitter metadata, JSON-LD, RSS, Sitemap, robots, 404, and a shared offline runtime. Markdown supports the built-in wikilink, embed, callout, hashtag, heading/TOC, highlighting, raw-HTML, math, Mermaid, and attachment behavior; unresolved links and unsupported syntax become deterministic diagnostics without hiding authored content.

YouTube/Vimeo URLs in Markdown image syntax remain image destinations; Obsite does not convert them into remote players. Use ordinary Markdown links for online videos. Authored external images and raw HTML can still initiate browser requests; fully offline content must use local resources.

The optional article metadata fields are `date`, `updated`, `tags`, `aliases`, `slug`, `order`, `author`, `reviewed`, `status`, `audience`, `productVersion`, `series`, `cover`, `banner`, and `bannerAlt`. Dates are normalized to UTC. A banner requires a non-empty matching `bannerAlt`, is published once through the asset planner, and appears only in its owning section or article header. Banners use a responsive `16:5` display ratio with `object-fit: cover` and a 280px maximum height. Covers are local PNG, JPEG, or WebP files and are used only as social-card source images.

`status: deprecated` emits an explicit text notice independent of color or JavaScript. Metadata is omitted when its optional source value is absent; no current time or filesystem fallback is inserted.

## Page-level social cards

Every published `doc`, `post`, and `page` article receives one deterministic 1200×630 PNG. Cards use the fixed text-first colors and geometry, an optional center-cropped cover, an embedded audited font, grapheme-aware truncation, and no clock, random, browser, system-font, CDN, or network input. The final URL is content-addressed under:

```text
assets/social/<sha256(canonical-article-url)>/<sha256(canonical-card-input)>-<sha256(png-bytes)>.png
```

Article `og:image` and `twitter:image` point to that generated local PNG with `summary_large_image`. Sections never receive article social cards, and banners are not implicitly used as cards.

The canonical card input has this fixed compact-JSON key order:

```text
canonicalURL, siteTitle, title, context, description?, date?, updated?, tags?,
aliases?, slug?, type?, order?, author?, reviewed?, status?, audience?,
productVersion?, series?, coverHash?, generator
```

`context` is the normalized section title followed by ` / ` and the version label when a version is present. The `?` fields are omitted when absent after trimming; `publish` controls card eligibility rather than identity. `coverHash` is the SHA-256 of the cover bytes, while the cover path and banner metadata are not card inputs. Dates are UTC RFC3339 values, lists retain their normalized order, and `order` is numeric. All strings are normalized before rendering, and the JSON is UTF-8, compact, and emitted in the order above. Changes to any included field, including non-visible metadata such as `description`, `reviewed`, or `type`, change the content-addressed URL; changes to visible values also change PNG bytes.

## Assets, themes, and offline runtime

All source assets pass through one contained asset planner. The planner owns Markdown images/embeds, raw HTML resources, recursive local CSS dependencies, banners, covers, local `defaultImg`, theme assets, and output collision checks. The generated output contains one shared content-addressed runtime, offline KaTeX/Mermaid resources, one optional Sidebar JSON, structural CSS, optional `.obsite/theme/theme.css`, and optional vault-root `custom.css`. Themes can change variables and append-only slots but cannot replace the HTML shell or renderer. No runtime resource is downloaded and no public Go SDK is provided.

Theme resources are planned during validation and published with content-addressed URLs. In slots, use `{{themeAssetURL .SiteRootRel "logo.svg"}}` for `.obsite/theme/assets/logo.svg`. Relative CSS URLs and imports are rewritten to planned resources: `theme.css` resolves them from `assets/`, and nested stylesheets from their own directory. Missing references and cyclic stylesheet dependencies are validation errors.

## Related recommendations

Related recommendations use one fixed dynamic TF-IDF, sparse-cosine, source-only link/tag algorithm. `related.enabled` is a boolean feature switch (`true` or `false`); `related.count` is an integer restricted to `1..20`, including when recommendations are disabled. Published `doc`, `post`, and `page` articles are ranked only against articles in the same version partition; sections and unpublished content do not affect DF, IDF, tags, or candidates.

The normative [related recommendation acceptance contract](docs/related-recommendations.md) fixes every formula, weight, threshold and limit, the tokenizer rules and resource hashes, source/candidate corpus boundaries, ranking order, and calibration evidence. These internals are not user-configurable.

## Install and development

Release archives contain one `CGO_ENABLED=0` executable, the project license, and third-party notices. Verify the published SHA-256 checksums. Source installation requires Go 1.25 or newer:

```bash
go install github.com/simp-lee/obsite/cmd/obsite@vX.Y.Z
```

Development checks use pinned Go, Node.js, Playwright, GoReleaser, actionlint, golangci-lint, gofumpt, and goimports versions:

```bash
make tools
make check
go test -race ./internal/analyze ./internal/build ./internal/cli ./internal/render ./internal/server ./internal/recommend/... ./internal/vault ./internal/link
npm ci
npm run build:editor
npx --no-install playwright install chromium
npm run test:e2e
```

Obsite does not provide site search, migration of old vault formats, complete template replacement, online search, a database, or external runtime downloads.
