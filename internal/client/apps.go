package client

import (
	"fmt"
	"strings"
)

// Apps are written through Drop's operations (ops.go: `drop app`, `drop
// secret`). Search reads the local full-text index of app names and slugs;
// like SQL it reads whole tables, so it works only with the owner's local key.

// appColumns are the SQL-readable app fields search returns, in output order.
const appColumns = "id, owner, name, slug, hostname, desired_state, archived, created, updated"

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
