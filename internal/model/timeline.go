package model

import (
	"strconv"
	"strings"

	"github.com/simp-lee/oxpio/internal/slug"
)

// TimelinePage identifies one generated timeline page and the slice of posts
// that it renders. Page numbers are one-based.
type TimelinePage struct {
	Number int
	Route  string
	Start  int
	End    int
}

// TimelinePagePlan is the canonical pagination plan shared by route planning,
// output generation, sitemap generation, and timeline rendering.
type TimelinePagePlan struct {
	BaseRoute string
	PageSize  int
	Pages     []TimelinePage
}

// NewTimelinePagePlan creates the complete plan for a timeline with total
// posts. It always creates one page, including when there are no posts.
func NewTimelinePagePlan(rawPath string, configuredPageSize, total int) *TimelinePagePlan {
	if total < 0 {
		total = 0
	}
	pageSize := configuredPageSize
	if pageSize <= 0 || pageSize >= total {
		pageSize = total
	}
	if pageSize == 0 {
		pageSize = 1
	}
	pageCount := (total + pageSize - 1) / pageSize
	if pageCount == 0 {
		pageCount = 1
	}

	baseRoute := "/" + slug.EncodePath(strings.Trim(rawPath, "/")) + "/"
	pages := make([]TimelinePage, 0, pageCount)
	for page := 1; page <= pageCount; page++ {
		start := (page - 1) * pageSize
		end := start + pageSize
		if end > total {
			end = total
		}
		route := baseRoute
		if page > 1 {
			route = strings.TrimSuffix(baseRoute, "/") + "/page/" + strconv.Itoa(page) + "/"
		}
		pages = append(pages, TimelinePage{Number: page, Route: route, Start: start, End: end})
	}
	return &TimelinePagePlan{BaseRoute: baseRoute, PageSize: pageSize, Pages: pages}
}

// Page returns the page with the requested one-based number.
func (plan *TimelinePagePlan) Page(number int) (TimelinePage, bool) {
	if plan == nil {
		return TimelinePage{}, false
	}
	for _, page := range plan.Pages {
		if page.Number == number {
			return page, true
		}
	}
	return TimelinePage{}, false
}

// PageForRoute returns the page represented by route.
func (plan *TimelinePagePlan) PageForRoute(route string) (TimelinePage, bool) {
	if plan == nil {
		return TimelinePage{}, false
	}
	for _, page := range plan.Pages {
		if page.Route == route {
			return page, true
		}
	}
	return TimelinePage{}, false
}
