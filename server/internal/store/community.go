package store

import (
	"errors"
	"net/url"
	"strings"
)

// Community interactions (image links, likes, comments) live in the very same
// document as the posts (recommendations.json, schema v1 superset). A historical
// post simply has no "images" / "likes" / "comments" key, which every reader here
// treats as empty — so old data keeps working with no migration step at all.
//
// The board is intentionally un-clever about storage: reads and writes go through
// loadRecommendDoc/saveRecommendDoc, exactly like plain posts do.
const (
	// CommunityMaxImages caps how many external image links one post may carry.
	CommunityMaxImages = 9
	// CommunityMaxImageLen caps the length of a single image link.
	CommunityMaxImageLen = 500
	// CommunityMaxComment caps one comment in runes.
	CommunityMaxComment = 500
	// CommunityMaxComments caps how many comments one post keeps (oldest dropped).
	CommunityMaxComments = 500
	// CommunityCommentPageSize is the default comment page size.
	CommunityCommentPageSize = 20
)

// normalizeImageLinks trims, validates and de-duplicates external image links.
// Only absolute http/https URLs are accepted: the server never accepts an upload
// and never stores image bytes, it just references remote links.
func normalizeImageLinks(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		link, ok := normalizeImageLink(raw)
		if !ok || seen[link] {
			continue
		}
		seen[link] = true
		out = append(out, link)
		if len(out) >= CommunityMaxImages {
			break
		}
	}
	return out
}

func normalizeImageLink(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > CommunityMaxImageLen {
		return "", false
	}
	p, err := url.Parse(s)
	if err != nil {
		return "", false
	}
	if p.Scheme != "http" && p.Scheme != "https" {
		return "", false
	}
	if strings.TrimSpace(p.Host) == "" {
		return "", false
	}
	return s, true
}

// RecommendImageURLs reads the stored image links of one post. It tolerates both
// the []string we write and the []any we read back from JSON, and skips anything
// that no longer passes validation.
func RecommendImageURLs(v any) []string {
	switch t := v.(type) {
	case []string:
		return normalizeImageLinks(t)
	case []any:
		raw := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				raw = append(raw, s)
			}
		}
		return normalizeImageLinks(raw)
	}
	return []string{}
}

func communityLikeMap(m map[string]any) map[string]any {
	likes, _ := m["likes"].(map[string]any)
	return likes
}

func communityCommentList(m map[string]any) []any {
	list, _ := m["comments"].([]any)
	return list
}

// RecommendPostStats returns the like count, comment count and whether viewer
// already liked the post. Counts are derived from the stored maps/slices, so a
// list response costs the browser nothing.
func RecommendPostStats(m map[string]any, viewer string) (int, int, bool) {
	likes := communityLikeMap(m)
	liked := false
	if v := strings.TrimSpace(viewer); v != "" {
		_, liked = likes[v]
	}
	return len(likes), len(communityCommentList(m)), liked
}

// RecommendLike toggles viewer's like on a post and reports the resulting state.
// The toggle is idempotent from the browser's point of view: a retry flips twice,
// so callers must send exactly one request per click.
func RecommendLike(user, postID string) (bool, int, error) {
	u := strings.TrimSpace(user)
	if u == "" {
		return false, 0, errors.New("Not authenticated")
	}
	pid := strings.TrimSpace(postID)
	if pid == "" {
		return false, 0, errors.New("Missing id")
	}
	doc := loadRecommendDoc()
	for _, v := range recommendPosts(doc) {
		m, ok := v.(map[string]any)
		if !ok || getStr(m, "id") != pid {
			continue
		}
		likes := communityLikeMap(m)
		if likes == nil {
			likes = map[string]any{}
		}
		liked := false
		if _, exists := likes[u]; exists {
			delete(likes, u)
		} else {
			likes[u] = nowMS()
			liked = true
		}
		if len(likes) == 0 {
			// Keep the document shaped like the legacy one when nothing is left.
			delete(m, "likes")
		} else {
			m["likes"] = likes
		}
		saveRecommendDoc(doc)
		return liked, len(likes), nil
	}
	return false, 0, errors.New("Post not found")
}

func publicComment(c map[string]any) map[string]any {
	return map[string]any{
		"id":         getStr(c, "id"),
		"body":       getStr(c, "body"),
		"author":     getStr(c, "author"),
		"created_at": int64(numF(c["created_at"])),
	}
}

// RecommendComments returns one page of a post's comments, newest first.
func RecommendComments(postID string, page, pageSize int) (map[string]any, error) {
	pid := strings.TrimSpace(postID)
	if pid == "" {
		return nil, errors.New("Missing id")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = CommunityCommentPageSize
	}
	if pageSize > 50 {
		pageSize = 50
	}
	doc := loadRecommendDoc()
	for _, v := range recommendPosts(doc) {
		m, ok := v.(map[string]any)
		if !ok || getStr(m, "id") != pid {
			continue
		}
		list := communityCommentList(m)
		total := len(list)
		start := total - page*pageSize
		if start < 0 {
			start = 0
		}
		end := total - (page-1)*pageSize
		if end > total {
			end = total
		}
		items := make([]map[string]any, 0, end-start)
		for i := end - 1; i >= start; i-- {
			c, ok := list[i].(map[string]any)
			if !ok {
				continue
			}
			items = append(items, publicComment(c))
		}
		return map[string]any{
			"items":     items,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
			"has_more":  start > 0,
		}, nil
	}
	return nil, errors.New("Post not found")
}

// RecommendCommentCreate appends a comment to a post and returns it.
func RecommendCommentCreate(user, postID, body string) (map[string]any, error) {
	u := strings.TrimSpace(user)
	if u == "" {
		return nil, errors.New("Not authenticated")
	}
	pid := strings.TrimSpace(postID)
	if pid == "" {
		return nil, errors.New("Missing id")
	}
	b := []rune(strings.TrimSpace(body))
	if len(b) == 0 {
		return nil, errors.New("Missing content")
	}
	if len(b) > CommunityMaxComment {
		b = b[:CommunityMaxComment]
	}
	doc := loadRecommendDoc()
	for _, v := range recommendPosts(doc) {
		m, ok := v.(map[string]any)
		if !ok || getStr(m, "id") != pid {
			continue
		}
		c := map[string]any{
			"id":         newRecommendID(),
			"body":       string(b),
			"author":     u,
			"created_at": nowMS(),
		}
		list := append(communityCommentList(m), c)
		if len(list) > CommunityMaxComments {
			list = list[len(list)-CommunityMaxComments:]
		}
		m["comments"] = list
		saveRecommendDoc(doc)
		return publicComment(c), nil
	}
	return nil, errors.New("Post not found")
}

// RecommendCommentDelete removes a comment when the caller wrote it or is a
// moderator. Comment ids are globally unique, so the scan stops at the first hit.
func RecommendCommentDelete(user, commentID string, admin bool) error {
	u := strings.TrimSpace(user)
	if u == "" {
		return errors.New("Not authenticated")
	}
	cid := strings.TrimSpace(commentID)
	if cid == "" {
		return errors.New("Missing id")
	}
	doc := loadRecommendDoc()
	found := false
	changed := false
	forbidden := false
	for _, v := range recommendPosts(doc) {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		list := communityCommentList(m)
		hit := false
		for _, cv := range list {
			if c, ok := cv.(map[string]any); ok && getStr(c, "id") == cid {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		found = true
		out := make([]any, 0, len(list))
		for _, cv := range list {
			c, ok := cv.(map[string]any)
			if ok && getStr(c, "id") == cid {
				if admin || getStr(c, "author") == u {
					changed = true
					continue
				}
				forbidden = true
			}
			out = append(out, cv)
		}
		if changed {
			if len(out) == 0 {
				delete(m, "comments")
			} else {
				m["comments"] = out
			}
		}
		break
	}
	if !found {
		return errors.New("Comment not found")
	}
	if forbidden {
		return errors.New("Forbidden")
	}
	if changed {
		saveRecommendDoc(doc)
	}
	return nil
}
