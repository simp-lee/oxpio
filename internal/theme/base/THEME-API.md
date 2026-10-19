# OXPIO Theme API

Themes are Go `html/template` files using `oxpio.theme.vm/v1`. The required
entries are `layouts/base.html`, `section.html`, `article.html`, `tag.html`,
`timeline.html`, and `404.html`; each page entry receives a page View Model
and may call the shared `base` template. A local theme must also contain the
`partials/` and `assets/` directories; either directory may be empty when the
theme has no partials or static assets.

Page kinds are `section`, `article`, `tag`, `timeline`, and `404`. Main fields
include `.Site`, `.Kind`, `.Title`, `.Description`, `.URL`, `.Href`,
`.Canonical`, `.BasePath`, `.Content`, `.Section`, `.Article`, `.Entries`,
`.Navigation`, `.Sidebar`, `.SidebarEnabled`, `.Breadcrumbs`, `.Versions`, `.Source`,
`.Pagination`, `.SEO`, `.VersionID`, `.HasMath`, `.HasMermaid`, `.NotFound`, and
`.Popover`.
`.Content` is core-rendered controlled HTML; Markdown remains the only content
source. Optional values are empty or nil rather than inferred.

## Runtime mount contract

When Sidebar is enabled, the rendered shell must include `data-site-body`,
`data-sidebar-shell`, `data-sidebar-root`, `data-sidebar-toggle`,
`data-sidebar-close`, and `data-sidebar-overlay`. When Popover is enabled it
must include one `<div id="..." data-popover-card>`. These markers are consumed by the shared
offline runtime and are checked by `theme validate`; the mobile toggle and
close/overlay controls must remain visible and usable at narrow widths.

`data-theme-toggle` is an optional visible control. If present, the shared
runtime reveals it and updates its `data-theme-toggle-value` child; themes that
provide manual color mode switching must also style `[data-theme="dark"]` and
`[data-theme="light"]`, not only `prefers-color-scheme`. Content previews keep
math and diagram markers but remove Sidebar and Popover markers because their
shell mount points are intentionally cropped.

The pure template functions are `siteURL(path)`, `routeURL(route)`,
`assetURL(themePath)`, `formatDate(value, layout)`, and
`emitJSONLD(value)`. `assetURL` accepts only a planned path such as
`assets/style.css`; it never reads files or the network. External `href`
navigation links are allowed, but external `src`, stylesheet, and CSS
`url()` resources are rejected so theme previews remain offline. Use `theme context`,
`theme validate`, and `theme preview` for the complete current contract and
feedback loop. Copy the embedded `base` theme with `theme init` before editing.
