package client

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The placeholder data model: the project's apps, each a name and a unique
// slug. Writes go through the records API; reads use the authenticated SQL
// reader and full-text search.

// MaxAppName is the longest app name, in characters.
const MaxAppName = 80

// slugPattern is the app slug rule, the same as the apps migration's.
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)

// appColumns are the SQL-readable app fields, in output order.
const appColumns = "id, name, slug, created, updated"

// AddApp creates one app.
func (c *Client) AddApp(name, slug string) (map[string]any, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxAppName {
		return nil, fail(2, "the name must be 1-%d characters of UTF-8 text", MaxAppName)
	}
	if !slugPattern.MatchString(slug) {
		return nil, fail(2, "the slug must be 3-32 lowercase letters, digits and hyphens, starting with a letter and not ending with a hyphen")
	}
	data, err := c.Must("POST", records("apps"), map[string]string{"name": name, "slug": slug})
	if err != nil {
		return nil, err
	}
	record, ok := data.(map[string]any)
	if !ok {
		return nil, fail(1, "unexpected response to the new app")
	}
	out := map[string]any{}
	for _, key := range strings.Split(appColumns, ", ") {
		out[key] = record[key]
	}
	return out, nil
}

// ListApps returns the newest apps.
func (c *Client) ListApps(limit int) ([]map[string]any, error) {
	if limit < 1 || limit > 500 {
		return nil, fail(2, "--limit must be 1-500")
	}
	return c.Rows(fmt.Sprintf("SELECT %s FROM apps ORDER BY created DESC, id DESC LIMIT %d", appColumns, limit))
}

// Search ranks the apps by full-text match of their name and slug.
func (c *Client) Search(term string, o SearchOptions) (map[string]any, error) {
	if strings.TrimSpace(term) == "" || len(term) > 4096 || len(strings.Fields(term)) > 16 {
		return nil, fail(2, "search requires 1-16 terms and at most 4096 UTF-8 bytes")
	}
	if o.Limit < 1 || o.Limit > 100 || o.Offset < 0 || o.Offset > 10000 {
		return nil, fail(2, "search limit must be 1-100 and offset 0-10000")
	}
	if o.Offset > 0 && o.Generation == "" {
		return nil, fail(2, "continuation requires --generation from the first page")
	}
	body := map[string]any{"index": "apps", "query": term, "limit": o.Limit, "offset": o.Offset}
	if o.Generation != "" {
		body["expectedGeneration"] = o.Generation
	}
	data, err := c.Must("POST", "/api/context/search", body)
	if err != nil {
		if f, ok := err.(*Fail); ok && f.Code == 4 {
			return nil, fail(4, "the apps changed; discard prior result pages and restart at offset 0 without --generation")
		}
		return nil, err
	}
	result, _ := data.(map[string]any)
	hits, _ := result["hits"].([]any)
	generation, _ := result["generation"].(string)
	hasMore, _ := result["hasMore"].(bool)
	if result["index"] != "apps" || generation == "" || (o.Generation != "" && generation != o.Generation) || len(hits) > o.Limit {
		return nil, fail(1, "invalid search response")
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		id, _ := dig(h, "id").(string)
		if !idPattern.MatchString(id) {
			return nil, fail(1, "invalid search hit")
		}
		ids = append(ids, literal(id))
	}
	byID := map[string]map[string]any{}
	if len(ids) > 0 {
		rows, err := c.Rows(fmt.Sprintf("SELECT %s FROM apps WHERE id IN (%s)", appColumns, strings.Join(ids, ",")))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			id, _ := row["id"].(string)
			byID[id] = row
		}
	}
	apps := []map[string]any{}
	for _, h := range hits {
		id, _ := dig(h, "id").(string)
		row, ok := byID[id]
		if !ok {
			continue // deleted after the search read
		}
		row["score"], row["excerpt"] = dig(h, "score"), dig(h, "excerpt")
		apps = append(apps, row)
	}
	var next any
	if hasMore {
		next = o.Offset + len(hits)
	}
	return map[string]any{"query": term, "generation": generation, "apps": apps, "hasMore": hasMore, "nextOffset": next}, nil
}
