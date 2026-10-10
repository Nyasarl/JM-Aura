package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"jmaura/internal/store"
)

// The community board (社区) reuses the /api/recommend family: the same posts,
// the same list/create/delete endpoints, plus likes and comments below. Keeping
// one API family means the older browsers and clients that already talk to
// /api/recommend keep working unchanged.

// communityImages validates the client-supplied external image links. Uploads are
// not supported at all: only absolute http/https URLs are accepted, they are kept
// as-is, and the server never fetches, proxies or stores image bytes.
func communityImages(in []string) ([]string, error) {
	if len(in) > store.CommunityMaxImages {
		return nil, errors.New("Too many images")
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if len(s) > store.CommunityMaxImageLen {
			return nil, errors.New("Image link too long")
		}
		p, err := url.Parse(s)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || strings.TrimSpace(p.Host) == "" {
			return nil, errors.New("Invalid image link")
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

// queryInt reads an integer query parameter, falling back to def when absent or
// malformed so a bad query string can never fail a read request.
func queryInt(r *http.Request, key string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// handleRecommendLike toggles the caller's like on a post. Registered site users
// only, which keeps likes tied to a stable identity. The response carries the
// resulting state so the browser can correct its optimistic update.
func handleRecommendLike(w http.ResponseWriter, r *http.Request) {
	u, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if !recommendLimiter.allow(remoteAddrKey(r), 40) {
		httpDetail(w, http.StatusTooManyRequests, "Too many requests")
		return
	}
	liked, count, err := store.RecommendLike(u, r.PathValue("id"))
	if err != nil {
		writeJSON(w, 200, errSt(StatusUserError, err.Error()))
		return
	}
	writeJSON(w, 200, ok(map[string]any{"liked": liked, "like_count": count}, ""))
}

// handleRecommendComments returns one page of a post's comments, newest first.
// Read access is public; can_delete / author_name are resolved here so the browser
// renders the thread without extra work.
func handleRecommendComments(w http.ResponseWriter, r *http.Request) {
	data, err := store.RecommendComments(
		r.PathValue("id"),
		queryInt(r, "page", 1),
		queryInt(r, "page_size", store.CommunityCommentPageSize),
	)
	if err != nil {
		writeJSON(w, 200, errSt(StatusUserError, err.Error()))
		return
	}
	me := plainSiteUserOf(r)
	moderator := isRecommendAdmin(me)
	nicks := store.JmNicknames()
	raw, _ := data["items"].([]map[string]any)
	items := make([]map[string]any, 0, len(raw))
	for _, c := range raw {
		author, _ := c["author"].(string)
		items = append(items, map[string]any{
			"id":          c["id"],
			"body":        c["body"],
			"author":      author,
			"author_name": recommendDisplayName(nicks, author),
			"created_at":  c["created_at"],
			"can_delete":  moderator || (me != "" && author == me),
		})
	}
	writeJSON(w, 200, ok(map[string]any{
		"items":     items,
		"total":     data["total"],
		"page":      data["page"],
		"page_size": data["page_size"],
		"has_more":  data["has_more"],
	}, ""))
}

// handleRecommendCommentCreate appends a comment to a post.
func handleRecommendCommentCreate(w http.ResponseWriter, r *http.Request) {
	u, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if !recommendLimiter.allow(remoteAddrKey(r), 20) {
		httpDetail(w, http.StatusTooManyRequests, "Too many requests")
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	c, err := store.RecommendCommentCreate(u, r.PathValue("id"), req.Body)
	if err != nil {
		writeJSON(w, 200, errSt(StatusUserError, err.Error()))
		return
	}
	author, _ := c["author"].(string)
	c["author_name"] = recommendDisplayName(store.JmNicknames(), author)
	c["can_delete"] = true
	writeJSON(w, 200, ok(c, ""))
}

// handleRecommendCommentDelete removes a comment the caller wrote, or any comment
// when the caller is a moderator.
func handleRecommendCommentDelete(w http.ResponseWriter, r *http.Request) {
	u, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if err := store.RecommendCommentDelete(u, r.PathValue("id"), isRecommendAdmin(u)); err != nil {
		writeJSON(w, 200, errSt(StatusUserError, err.Error()))
		return
	}
	writeJSON(w, 200, ok(map[string]any{"status": "success"}, ""))
}
