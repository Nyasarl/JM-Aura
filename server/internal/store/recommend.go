package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	RecommendMaxBody  = 1000
	recommendMaxTitle = 200
	recommendMaxPosts = 5000
)

func recommendFile() string {
	if v := os.Getenv("JM_AURA_RECOMMEND_PATH"); v != "" {
		return v
	}
	return "recommendations.json"
}

func loadRecommendDoc() map[string]any {
	if doc, ok := LoadJSON(recommendFile()); ok {
		if _, ok := doc["posts"].([]any); !ok {
			doc["posts"] = []any{}
		}
		return doc
	}
	return map[string]any{"v": 1, "posts": []any{}}
}

func saveRecommendDoc(doc map[string]any) {
	doc["v"] = 1
	_ = SaveJSON(recommendFile(), doc)
}

func recommendPosts(doc map[string]any) []any {
	posts, _ := doc["posts"].([]any)
	if posts == nil {
		posts = []any{}
		doc["posts"] = posts
	}
	return posts
}

func newRecommendID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(nowMS(), 36)
	}
	return hex.EncodeToString(b)
}

// normalizeRecommendKind keeps the stored kind within {"comic","novel"},
// defaulting to "comic" for legacy posts and unknown values.
func normalizeRecommendKind(kind string) string {
	if strings.EqualFold(strings.TrimSpace(kind), "novel") {
		return "novel"
	}
	return "comic"
}

// normalizeRecommendSource keeps the stored source within {"jm","bika"},
// defaulting to "jm" for legacy posts and unknown values.
func normalizeRecommendSource(source string) string {
	if strings.EqualFold(strings.TrimSpace(source), "bika") {
		return "bika"
	}
	return "jm"
}

// RecommendCreate appends a post. Text is trimmed and rune-capped; duplicates
// of the same comic_id are allowed by design. Title is a caller-resolved content
// name snapshot (may be empty when it could not be resolved); kind marks whether
// the target is a comic or a novel; source marks which upstream it belongs to.
// coverURL is an optional resolved cover snapshot: bika covers cannot be derived
// from the id alone, so they are persisted here.
//
// comicID may be empty: the board also takes plain 说说-style posts that
// recommend no work at all, as long as they carry text or an image link.
// images are external http/https links only (uploads are not supported); they are
// validated, de-duplicated and capped here as a last line of defence.
func RecommendCreate(author, comicID, title, kind, body, source, coverURL string, images []string) (map[string]any, error) {
	a := strings.TrimSpace(author)
	if a == "" {
		return nil, errors.New("Not authenticated")
	}
	cid := strings.TrimSpace(comicID)
	imgs := normalizeImageLinks(images)
	t := []rune(strings.TrimSpace(title))
	if len(t) > recommendMaxTitle {
		t = t[:recommendMaxTitle]
	}
	b := []rune(strings.TrimSpace(body))
	if len(b) == 0 && len(imgs) == 0 {
		return nil, errors.New("Missing content")
	}
	if len(b) > RecommendMaxBody {
		b = b[:RecommendMaxBody]
	}
	if cid == "" {
		// Pure status post: there is no work behind it, so no content title either.
		t = nil
	}
	doc := loadRecommendDoc()
	posts := recommendPosts(doc)
	post := map[string]any{
		"id":          newRecommendID(),
		"comic_id":    cid,
		"comic_title": string(t),
		"kind":        normalizeRecommendKind(kind),
		"source":      normalizeRecommendSource(source),
		"body":        string(b),
		"author":      a,
		"created_at":  nowMS(),
	}
	if len(imgs) > 0 {
		post["images"] = imgs
	}
	if cov := strings.TrimSpace(coverURL); cov != "" {
		post["cover_url"] = cov
	}
	posts = append(posts, post)
	if len(posts) > recommendMaxPosts {
		posts = posts[len(posts)-recommendMaxPosts:]
	}
	doc["posts"] = posts
	saveRecommendDoc(doc)
	return post, nil
}

// RecommendList returns posts sorted by created_at desc, paginated. viewer is the
// plain site user id (may be empty) and only drives the per-post liked_by_me flag;
// like/comment counts are computed here so the browser renders them without any
// client-side aggregation.
func RecommendList(page, pageSize int, viewer string) map[string]any {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 50 {
		pageSize = 50
	}
	doc := loadRecommendDoc()
	posts := recommendPosts(doc)
	list := make([]map[string]any, 0, len(posts))
	for _, v := range posts {
		if m, ok := v.(map[string]any); ok {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return numF(list[i]["created_at"]) > numF(list[j]["created_at"])
	})
	total := len(list)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items := make([]map[string]any, 0, end-start)
	for _, m := range list[start:end] {
		likes, comments, likedByMe := RecommendPostStats(m, viewer)
		items = append(items, map[string]any{
			"id":            getStr(m, "id"),
			"comic_id":      getStr(m, "comic_id"),
			"comic_title":   getStr(m, "comic_title"),
			"kind":          normalizeRecommendKind(getStr(m, "kind")),
			"source":        normalizeRecommendSource(getStr(m, "source")),
			"body":          getStr(m, "body"),
			"author":        getStr(m, "author"),
			"created_at":    int64(numF(m["created_at"])),
			"cover_url":     getStr(m, "cover_url"),
			"images":        RecommendImageURLs(m["images"]),
			"like_count":    likes,
			"comment_count": comments,
			"liked_by_me":   likedByMe,
		})
	}
	return map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  end < total,
	}
}

// RecommendDelete removes a post when the caller is its author or a moderator.
func RecommendDelete(user, id string, admin bool) error {
	u := strings.TrimSpace(user)
	if u == "" {
		return errors.New("Not authenticated")
	}
	pid := strings.TrimSpace(id)
	if pid == "" {
		return errors.New("Missing id")
	}
	doc := loadRecommendDoc()
	posts := recommendPosts(doc)
	out := make([]any, 0, len(posts))
	found := false
	forbidden := false
	for _, v := range posts {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if getStr(m, "id") == pid {
			found = true
			if admin || getStr(m, "author") == u {
				continue
			}
			forbidden = true
		}
		out = append(out, v)
	}
	if !found {
		return errors.New("Post not found")
	}
	if forbidden {
		return errors.New("Permission denied")
	}
	doc["posts"] = out
	saveRecommendDoc(doc)
	return nil
}
