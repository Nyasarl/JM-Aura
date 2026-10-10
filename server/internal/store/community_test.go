package store

import (
	"os"
	"path/filepath"
	"testing"
)

// newRecommendTestFile points the recommend/community store at a throwaway file
// and returns its path. The store resolves an absolute JM_AURA_RECOMMEND_PATH
// as-is, so no data directory is touched.
func newRecommendTestFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "recommendations.json")
	t.Setenv("JM_AURA_RECOMMEND_PATH", p)
	return p
}

func firstItem(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	items, _ := data["items"].([]map[string]any)
	if len(items) == 0 {
		t.Fatalf("expected at least one item, got %#v", data)
	}
	return items[0]
}

func TestRecommendCreateAcceptsPlainPostWithImages(t *testing.T) {
	newRecommendTestFile(t)

	post, err := RecommendCreate("alice", "", "", "", "今天把攒的三本一口气看完了，爽。", "jm", "", []string{
		" https://img.example.com/a.png ",
		"https://img.example.com/a.png", // duplicate
		"ftp://img.example.com/b.png",   // wrong scheme
		"javascript:alert(1)",           // not a link
		"https://img.example.com/c.png",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := post["comic_id"]; got != "" {
		t.Fatalf("comic_id = %#v, want empty", got)
	}
	if got := post["comic_title"]; got != "" {
		t.Fatalf("comic_title = %#v, want empty for a plain post", got)
	}
	imgs := RecommendImageURLs(post["images"])
	if len(imgs) != 2 || imgs[0] != "https://img.example.com/a.png" || imgs[1] != "https://img.example.com/c.png" {
		t.Fatalf("images = %#v, want trimmed and de-duplicated", imgs)
	}
}

func TestRecommendCreateRejectsEmptyPost(t *testing.T) {
	newRecommendTestFile(t)
	if _, err := RecommendCreate("alice", "", "", "comic", "   ", "jm", "", nil); err == nil {
		t.Fatal("expected an error for a post with neither text nor image")
	}
	if _, err := RecommendCreate("", "", "", "comic", "hi", "jm", "", nil); err == nil {
		t.Fatal("expected an error for an anonymous post")
	}
}

func TestRecommendListCountsAndLegacyCompatibility(t *testing.T) {
	p := newRecommendTestFile(t)

	// A document written by the previous release: no images / likes / comments.
	legacy := `{"v":1,"posts":[{"id":"legacy000000000000000001","comic_id":"12345","comic_title":"旧帖",
		"kind":"comic","source":"jm","body":"旧的阅读笔记","author":"bob","created_at":1700000000000}]}`
	if err := os.WriteFile(p, []byte(legacy), 0o600); err != nil {
		t.Fatalf("seed legacy doc: %v", err)
	}

	data := RecommendList(1, 10, "alice")
	it := firstItem(t, data)
	if it["like_count"] != 0 || it["comment_count"] != 0 || it["liked_by_me"] != false {
		t.Fatalf("legacy post stats = %#v/%#v/%#v, want 0/0/false", it["like_count"], it["comment_count"], it["liked_by_me"])
	}
	if imgs, _ := it["images"].([]string); len(imgs) != 0 {
		t.Fatalf("legacy post images = %#v, want empty", it["images"])
	}
	if it["body"] != "旧的阅读笔记" || it["comic_id"] != "12345" {
		t.Fatalf("legacy post lost fields: %#v", it)
	}

	if _, _, err := RecommendLike("alice", "legacy000000000000000001"); err != nil {
		t.Fatalf("like legacy post: %v", err)
	}
	it = firstItem(t, RecommendList(1, 10, "alice"))
	if it["like_count"] != 1 || it["liked_by_me"] != true {
		t.Fatalf("after like: count=%#v liked_by_me=%#v", it["like_count"], it["liked_by_me"])
	}
	other := firstItem(t, RecommendList(1, 10, "bob"))
	if other["like_count"] != 1 || other["liked_by_me"] != false {
		t.Fatalf("other viewer: count=%#v liked_by_me=%#v", other["like_count"], other["liked_by_me"])
	}
}

func TestRecommendLikeTogglesAndIsPerUser(t *testing.T) {
	newRecommendTestFile(t)
	post, err := RecommendCreate("alice", "", "", "comic", "第一帖", "jm", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, _ := post["id"].(string)

	if liked, count, err := RecommendLike("alice", id); err != nil || !liked || count != 1 {
		t.Fatalf("first like = %v/%d/%v, want true/1/nil", liked, count, err)
	}
	if liked, count, err := RecommendLike("bob", id); err != nil || !liked || count != 2 {
		t.Fatalf("second liker = %v/%d/%v, want true/2/nil", liked, count, err)
	}
	if liked, count, err := RecommendLike("alice", id); err != nil || liked || count != 1 {
		t.Fatalf("unlike = %v/%d/%v, want false/1/nil", liked, count, err)
	}
	if _, _, err := RecommendLike("alice", "000000000000000000000000"); err == nil {
		t.Fatal("expected an error liking an unknown post")
	}
	if _, _, err := RecommendLike("", id); err == nil {
		t.Fatal("expected an error for an anonymous like")
	}
}

func TestRecommendCommentsPagingAndDeletion(t *testing.T) {
	newRecommendTestFile(t)
	post, err := RecommendCreate("alice", "", "", "comic", "第一帖", "jm", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, _ := post["id"].(string)

	ids := make([]string, 0, 3)
	for _, body := range []string{"一", "二", "三"} {
		c, err := RecommendCommentCreate("bob", id, body)
		if err != nil {
			t.Fatalf("comment %s: %v", body, err)
		}
		ids = append(ids, c["id"].(string))
	}

	page1, err := RecommendComments(id, 1, 2)
	if err != nil {
		t.Fatalf("comments: %v", err)
	}
	items, _ := page1["items"].([]map[string]any)
	if len(items) != 2 || items[0]["body"] != "三" || items[1]["body"] != "二" {
		t.Fatalf("page1 = %#v, want newest first", items)
	}
	if page1["total"] != 3 || page1["has_more"] != true {
		t.Fatalf("page1 meta = %#v", page1)
	}
	page2, _ := RecommendComments(id, 2, 2)
	items2, _ := page2["items"].([]map[string]any)
	if len(items2) != 1 || items2[0]["body"] != "一" || page2["has_more"] != false {
		t.Fatalf("page2 = %#v meta=%#v", items2, page2)
	}

	if err := RecommendCommentDelete("carol", ids[0], false); err == nil {
		t.Fatal("expected a foreign delete to be refused")
	}
	if err := RecommendCommentDelete("bob", ids[0], false); err != nil {
		t.Fatalf("author delete: %v", err)
	}
	if err := RecommendCommentDelete("carol", ids[1], true); err != nil {
		t.Fatalf("moderator delete: %v", err)
	}
	if err := RecommendCommentDelete("bob", ids[0], false); err == nil {
		t.Fatal("expected an error deleting an unknown comment")
	}
	left, _ := RecommendComments(id, 1, 10)
	if left["total"] != 1 {
		t.Fatalf("total after deletes = %#v, want 1", left["total"])
	}

	// The post itself still reports its live comment count.
	it := firstItem(t, RecommendList(1, 10, ""))
	if it["comment_count"] != 1 {
		t.Fatalf("comment_count = %#v, want 1", it["comment_count"])
	}
}

func TestRecommendPostStatsIgnoresForeignViewer(t *testing.T) {
	newRecommendTestFile(t)
	post, err := RecommendCreate("alice", "12345", "某本子", "comic", "推荐", "jm", "", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id, _ := post["id"].(string)
	if _, _, err := RecommendLike("alice", id); err != nil {
		t.Fatalf("like: %v", err)
	}
	likes, comments, liked := RecommendPostStats(post, "")
	if likes != 0 || comments != 0 || liked {
		t.Fatalf("raw post stats = %d/%d/%v, want 0/0/false", likes, comments, liked)
	}
	it := firstItem(t, RecommendList(1, 10, "alice"))
	if it["comic_title"] != "某本子" || it["like_count"] != 1 || it["liked_by_me"] != true {
		t.Fatalf("item = %#v", it)
	}
}

func TestRecommendImageCapAndLength(t *testing.T) {
	newRecommendTestFile(t)
	links := make([]string, 0, 12)
	for i := 0; i < 12; i++ {
		links = append(links, "https://img.example.com/"+string(rune('a'+i))+".png")
	}
	post, err := RecommendCreate("alice", "", "", "comic", "", "jm", "", links)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := RecommendImageURLs(post["images"]); len(got) != CommunityMaxImages {
		t.Fatalf("images = %d, want capped at %d", len(got), CommunityMaxImages)
	}
	long := "https://img.example.com/" + string(make([]byte, CommunityMaxImageLen))
	post, err = RecommendCreate("alice", "", "", "comic", "只有超长图链", "jm", "", []string{long})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := RecommendImageURLs(post["images"]); len(got) != 0 {
		t.Fatalf("over-long link kept: %#v", got)
	}
}
