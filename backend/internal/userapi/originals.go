package userapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"net/url"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/rawartifacts"
	"github.com/jackc/pgx/v5"
)

const maxOriginalBytes = 32 << 20

type originalDownload struct {
	data                            []byte
	contentType, filename, redirect string
}

// A document ID alone never grants access: require a currently accessible
// owned Insight and a version actually used in that Insight's saved evidence.
func (h productHandler) original(ctx context.Context, user string, r *http.Request) (any, int, error) {
	if e := query(r); e != nil {
		return nil, 0, e
	}
	insightID, e := pathID(r, "insight_id")
	if e != nil {
		return nil, 0, e
	}
	versionID, e := pathID(r, "document_version_id")
	if e != nil {
		return nil, 0, e
	}
	_, snapshot, e := h.store.insight(ctx, user, insightID)
	if e != nil {
		return nil, 0, e
	}
	var chunk object
	for _, value := range array(snapshot["chunks"]) {
		candidate := obj(value)
		if candidate["document_version_id"] == versionID {
			chunk = candidate
			break
		}
	}
	if chunk == nil {
		return nil, 0, pgx.ErrNoRows
	}
	if address := str(chunk["canonical_url"]); address != "" {
		target, err := url.Parse(address)
		if err == nil && (target.Scheme == "https" || target.Scheme == "http") && target.Host != "" && target.User == nil {
			return originalDownload{redirect: target.String()}, http.StatusTemporaryRedirect, nil
		}
	}
	var artifact rawartifacts.Artifact
	var uri *string
	var title string
	e = h.store.pool.QueryRow(ctx, `SELECT a.sha256,a.content_type,a.size_bytes,CASE WHEN a.size_bytes<=$2 THEN a.content END,a.object_uri,v.title
 FROM article_versions v JOIN raw_artifacts a ON a.id=v.raw_artifact_id
 WHERE v.id=$1::uuid`, versionID, maxOriginalBytes).Scan(&artifact.SHA256, &artifact.ContentType, &artifact.Size, &artifact.Content, &uri, &title)
	if e != nil {
		return nil, 0, e
	}
	if artifact.Size < 0 || artifact.Size > maxOriginalBytes {
		return nil, 0, fail(413, "PAYLOAD_TOO_LARGE", "original is too large to download")
	}
	data := artifact.Content
	if uri != nil {
		if h.store.ReadOriginal == nil {
			return nil, 0, unavailable()
		}
		artifact.ObjectURI = *uri
		data, e = h.store.ReadOriginal(ctx, artifact)
		if e != nil {
			return nil, 0, unavailable()
		}
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != artifact.Size || hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, 0, fail(503, "SOURCE_UNAVAILABLE", "original integrity check failed")
	}
	filename := title
	if artifact.ContentType == "application/pdf" {
		filename += ".pdf"
	}
	return originalDownload{data: data, contentType: artifact.ContentType, filename: filename}, 200, nil
}

func writeOriginal(w http.ResponseWriter, r *http.Request, original originalDownload) {
	w.Header().Set("Cache-Control", "private, no-store")
	if original.redirect != "" {
		http.Redirect(w, r, original.redirect, http.StatusTemporaryRedirect)
		return
	}
	w.Header().Set("Content-Type", original.contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": original.filename}))
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprint(len(original.data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(original.data)
	}
}
