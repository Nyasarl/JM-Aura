package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"jmaura/internal/bika"
	"jmaura/internal/store"
)

var recommendLimiter = newRateLimiter(time.Minute)

func recommendAdminName() string {
	if v := strings.TrimSpace(os.Getenv("JM_AURA_RECOMMEND_ADMIN")); v != "" {
		return v
	}
	return "Tom6814"
}

func isRecommendAdmin(u string) bool {
	s := strings.TrimSpace(u)
	if s == "" {
		return false
	}
	if store.IsAdmin(s) {
		return true
	}
	return strings.EqualFold(s, recommendAdminName())
}

func validComicID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 12 {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// validRecommendID 按源校验作品 ID：JM 为纯数字（≤12 位），哔咔为十六进制串。
func validRecommendID(source, id string) bool {
	if source == bikaSource {
		s := strings.TrimSpace(id)
		if len(s) < 6 || len(s) > 32 {
			return false
		}
		for _, ch := range s {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
		return true
	}
	return validComicID(id)
}

// recommendTarget resolves a post target by probing JM: comics (/album) are
// tried first, then novels (/novel). Best-effort: on failure kind falls back to
// "comic" and the title may be empty so posting stays permissive.
func recommendTarget(ctx context.Context, identity, id string) (string, string) {
	aq := url.Values{}
	aq.Set("comicName", "")
	aq.Set("id", id)
	if res, err := apiClient().APIGet(ctx, "/album", aq, store.LoadCookies(identity)); err == nil {
		if t := strings.TrimSpace(pyStr(adaptAlbumDetail(decodeRawMap(res.Data))["title"])); t != "" {
			return "comic", t
		}
	}
	if res, err := apiClient().NovelDetail(ctx, id, store.LoadCookies(identity)); err == nil {
		if t := strings.TrimSpace(pyStr(adaptNovelDetail(decodeRawMap(res.Data))["title"])); t != "" {
			return "novel", t
		}
	}
	return "comic", ""
}

// recommendTargetOfKind resolves a post target honoring the kind explicitly
// chosen by the user ("comic" / "novel"). An empty or unknown kind falls back
// to auto-detection so older clients keep working. Best-effort: on probe
// failure the chosen kind is kept and the title may be empty.
func recommendTargetOfKind(ctx context.Context, identity, id, kind string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "novel":
		if res, err := apiClient().NovelDetail(ctx, id, store.LoadCookies(identity)); err == nil {
			if t := strings.TrimSpace(pyStr(adaptNovelDetail(decodeRawMap(res.Data))["title"])); t != "" {
				return "novel", t
			}
		}
		return "novel", ""
	case "comic":
		aq := url.Values{}
		aq.Set("comicName", "")
		aq.Set("id", id)
		if res, err := apiClient().APIGet(ctx, "/album", aq, store.LoadCookies(identity)); err == nil {
			if t := strings.TrimSpace(pyStr(adaptAlbumDetail(decodeRawMap(res.Data))["title"])); t != "" {
				return "comic", t
			}
		}
		return "comic", ""
	default:
		return recommendTarget(ctx, identity, id)
	}
}

// recommendTargetBika 用哔咔详情解析标题与封面快照。Best-effort：失败时返回空值，
// 发布保持宽容（标题留空、封面回落到占位图）。
func recommendTargetBika(ctx context.Context, identity, id string) (string, string) {
	data, err := bikaCall(ctx, identity, func(t string) (any, error) {
		return bikaClient.ComicDetail(ctx, t, id)
	})
	if err != nil {
		return "", ""
	}
	comic := bika.AsMap(bika.AsMap(data)["comic"])
	if comic == nil {
		comic = bika.AsMap(data)
	}
	if comic == nil {
		return "", ""
	}
	return strings.TrimSpace(bika.AsStr(comic["title"])), strings.TrimSpace(bikaCoverRaw(comic))
}

// recommendCoverURL builds the cover URL for the resolved kind so comics and
// novels resolve to their respective media directories. bika posts carry a
// stored cover snapshot instead, so there is nothing to derive here.
func recommendCoverURL(source, kind, id string) string {
	if source == bikaSource {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(kind), "novel") {
		return novelCoverURL(id)
	}
	return coverURL(id)
}

// recommendDisplayName prefers the JM nickname, falling back to the raw site username.
func recommendDisplayName(nicks map[string]string, author string) string {
	if n := strings.TrimSpace(nicks[author]); n != "" {
		return n
	}
	return strings.TrimSpace(author)
}

func handleRecommendList(w http.ResponseWriter, r *http.Request) {
	page := 1
	if p := strings.TrimSpace(r.URL.Query().Get("page")); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			page = n
		}
	}
	size := 10
	if s := strings.TrimSpace(r.URL.Query().Get("page_size")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			size = n
		}
	}
	me := plainSiteUserOf(r)
	moderator := isRecommendAdmin(me)
	data := store.RecommendList(page, size, me)
	raw, _ := data["items"].([]map[string]any)
	nicks := store.JmNicknames()
	items := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		cid, _ := it["comic_id"].(string)
		author, _ := it["author"].(string)
		kind, _ := it["kind"].(string)
		if kind != "novel" {
			kind = "comic"
		}
		source, _ := it["source"].(string)
		if source != bikaSource {
			source = "jm"
		}
		cover, _ := it["cover_url"].(string)
		if strings.TrimSpace(cover) == "" && cid != "" {
			// A work-less 说说帖 has nothing to derive a cover from.
			cover = recommendCoverURL(source, kind, cid)
		}
		items = append(items, map[string]any{
			"id":            it["id"],
			"comic_id":      cid,
			"comic_title":   it["comic_title"],
			"kind":          kind,
			"source":        source,
			"body":          it["body"],
			"author":        author,
			"author_name":   recommendDisplayName(nicks, author),
			"created_at":    it["created_at"],
			"cover_url":     cover,
			"images":        it["images"],
			"like_count":    it["like_count"],
			"comment_count": it["comment_count"],
			"liked_by_me":   it["liked_by_me"],
			"can_delete":    moderator || (me != "" && author == me),
		})
	}
	writeJSON(w, 200, ok(map[string]any{
		"items":        items,
		"total":        data["total"],
		"page":         data["page"],
		"page_size":    data["page_size"],
		"has_more":     data["has_more"],
		"is_moderator": moderator,
	}, ""))
}

func handleRecommendCreate(w http.ResponseWriter, r *http.Request) {
	u, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	if !recommendLimiter.allow(remoteAddrKey(r), 5) {
		httpDetail(w, http.StatusTooManyRequests, "Rate limit exceeded")
		return
	}
	var req struct {
		ComicID string   `json:"comic_id"`
		Kind    string   `json:"kind"`
		Source  string   `json:"source"`
		Body    string   `json:"body"`
		Images  []string `json:"images"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	source := "jm"
	if strings.EqualFold(strings.TrimSpace(req.Source), bikaSource) {
		source = bikaSource
	}
	images, err := communityImages(req.Images)
	if err != nil {
		writeJSON(w, 200, errSt(StatusUserError, err.Error()))
		return
	}
	comicID := strings.TrimSpace(req.ComicID)
	var kind, title, cover string
	if comicID != "" {
		if !validRecommendID(source, comicID) {
			writeJSON(w, 200, errSt(StatusUserError, "Invalid comic_id"))
			return
		}
		if source == bikaSource {
			// 哔咔只有漫画，类型固定为 comic；标题 / 封面按该源身份解析。
			kind = "comic"
			title, cover = recommendTargetBika(r.Context(), effIdentityFor(r, source), comicID)
		} else {
			kind, title = recommendTargetOfKind(r.Context(), effIdentityOf(r), comicID, req.Kind)
		}
	} else {
		// 纯说说帖：没有关联作品，类型只用于兜底展示。
		kind = "comic"
	}
	post, err := store.RecommendCreate(u, comicID, title, kind, req.Body, source, cover, images)
	if err != nil {
		writeJSON(w, 200, errSt(StatusUserError, err.Error()))
		return
	}
	cid, _ := post["comic_id"].(string)
	if cov, _ := post["cover_url"].(string); strings.TrimSpace(cov) == "" && cid != "" {
		post["cover_url"] = recommendCoverURL(source, kind, cid)
	}
	post["source"] = source
	post["author_name"] = recommendDisplayName(store.JmNicknames(), u)
	post["images"] = store.RecommendImageURLs(post["images"])
	post["like_count"] = 0
	post["comment_count"] = 0
	post["liked_by_me"] = false
	post["can_delete"] = true
	writeJSON(w, 200, ok(map[string]any{"status": "success", "post": post}, ""))
}

func handleRecommendDelete(w http.ResponseWriter, r *http.Request) {
	u, okc := requireSiteUser(w, r)
	if !okc {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSON(w, 200, errSt(StatusUserError, "Missing id"))
		return
	}
	if err := store.RecommendDelete(u, id, isRecommendAdmin(u)); err != nil {
		writeJSON(w, 200, errSt(StatusUserError, err.Error()))
		return
	}
	writeJSON(w, 200, ok(map[string]any{"status": "success"}, ""))
}
