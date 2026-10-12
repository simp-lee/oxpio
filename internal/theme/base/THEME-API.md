# OXPIO Theme API

Themes are Go `html/template` files using `oxpio.theme.vm/v1`. The required
entries are `layouts/base.html`, `section.html`, `article.html`, `tag.html`,
`timeline.html`, and `404.html`; each page entry receives a page View Model
and may call the shared `base` template.

Page kinds are `section`, `article`, `tag`, `timeline`, and `404`. Main fields
include `.Site`, `.Kind`, `.Title`, `.Description`, `.URL`, `.Href`,
`.Canonical`, `.BasePath`, `.Content`, `.Section`, `.Article`, `.Entries`,
`.Navigation`, `.Sidebar`, `.Breadcrumbs`, `.Versions`, `.Source`,
`.Pagination`, and `.SEO`. `.Content` is core-rendered controlled HTML; Markdown remains the only content
source. Optional values are
empty or nil rather than inferred.

The pure template functions are `siteURL(path)`, `routeURL(route)`,
`assetURL(themePath)`, `formatDate(value, layout)`, and
`emitJSONLD(value)`. `assetURL` accepts only a planned path such as
`assets/style.css`; it never reads files or the network. Use `theme context`,
`theme validate`, and `theme preview` for the complete current contract and
feedback loop. Copy the embedded `base` theme with `theme init` before editing.
