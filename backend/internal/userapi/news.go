package userapi

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

type newsItem struct {
	ArticleID        string     `json:"article_id"`
	ArticleVersionID string     `json:"article_version_id"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary"`
	SourceName       string     `json:"source_name"`
	TextOrigin       string     `json:"text_origin"`
	URL              *string    `json:"url"`
	PublishedAt      *time.Time `json:"published_at"`
	FetchedAt        time.Time  `json:"fetched_at"`
}

func (h productHandler) news(ctx context.Context, _ string, r *http.Request) (any, int, error) {
	if err := query(r); err != nil {
		return nil, 0, err
	}
	rows, err := h.store.pool.Query(ctx, `
		SELECT a.id::text, v.id::text,
		       left(COALESCE(NULLIF(btrim(v.title), ''), btrim(v.body_text)), 240),
		       left(regexp_replace(btrim(v.body_text), '[[:space:]]+', ' ', 'g'), 360),
		       left(s.name, 120), v.text_origin,
		       CASE WHEN length(a.canonical_url) <= 2048 THEN a.canonical_url ELSE NULL END,
		       v.published_at, v.fetched_at
		FROM articles a
		JOIN article_versions v ON v.id = a.latest_version_id
		JOIN sources s ON s.id = a.source_id
		WHERE a.latest_version_id IS NOT NULL
		  AND s.enabled AND s.source_role = 'external' AND s.credential_ref IS NULL
		  AND length(btrim(v.body_text)) > 0
		ORDER BY a.first_seen_at DESC, a.id DESC
		LIMIT 64`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]newsItem, 0, 64)
	for rows.Next() {
		var item newsItem
		if err := rows.Scan(&item.ArticleID, &item.ArticleVersionID, &item.Title, &item.Summary,
			&item.SourceName, &item.TextOrigin, &item.URL, &item.PublishedAt, &item.FetchedAt); err != nil {
			return nil, 0, err
		}
		if item.URL != nil {
			parsed, err := url.Parse(*item.URL)
			if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
				item.URL = nil
			}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return object{"items": items}, http.StatusOK, nil
}
