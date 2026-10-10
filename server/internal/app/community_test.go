package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jmaura/internal/store"
)

// These tests drive the real router (so pattern registration and auth are
// exercised for real) with a throwaway data directory and hand-made sessions,
// which keeps the whole community flow offline: no upstream JM/bika call happens
// as long as a post carries no comic_id or an invalid one.
var communityDataDir string
var ipCounter int

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jmaura-community-test")
	if err != nil {
		panic(err)
	}
	communityDataDir = dir
	// Pin the data directory: without this the store would resolve the repo's own
	// config/ directory and a test run would write into real data.
	store.SetDataDir(dir)
	os.Setenv("JM_AURA_RECOMMEND_ADMIN", "mod")
	for _, u := range []struct {
		name  string
		admin bool
	}{{"alice", false}, {"bob", false}, {"mod", true}} {
		if err := store.CreateUser(u.name, "pw-"+u.name, u.admin); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type communityFixture struct {
	t   *testing.T
	mux http.Handler
}

// seedPosts replaces the board document, so one test's posts can never leak into
// the next one.
func seedPosts(t *testing.T, doc string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(communityDataDir, "recommendations.json"), []byte(doc), 0o600); err != nil {
		t.Fatalf("seed recommendations: %v", err)
	}
}

func newCommunityFixture(t *testing.T) *communityFixture {
	t.Helper()
	seedPosts(t, `{"v":1,"posts":[]}`)
	// Wrap() is where the request identity (site user / guest) is resolved into the
	// request context, so tests must go through it exactly like main.go does.
	return &communityFixture{t: t, mux: Wrap(NewRouter())}
}

func (f *communityFixture) sid(t *testing.T, user string) string {
	t.Helper()
	sid, err := store.CreateSession(user)
	if err != nil {
		t.Fatalf("session for %s: %v", user, err)
	}
	return sid
}

type envelope struct {
	St   int             `json:"st"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (f *communityFixture) call(method, path, body, sid string) (*httptest.ResponseRecorder, envelope) {
	f.t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// Give every request its own client IP: the write endpoints are rate limited
	// per IP and a shared fake address would trip the limiter inside one test run.
	ipCounter++
	req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:1234", ipCounter>>16&0xff, ipCounter>>8&0xff, ipCounter&0xff)
	if sid != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		f.t.Fatalf("%s %s: bad json %q: %v", method, path, rec.Body.String(), err)
	}
	return rec, env
}

type postItem struct {
	ID           string   `json:"id"`
	ComicID      string   `json:"comic_id"`
	ComicTitle   string   `json:"comic_title"`
	Body         string   `json:"body"`
	Author       string   `json:"author"`
	Images       []string `json:"images"`
	LikeCount    int      `json:"like_count"`
	CommentCount int      `json:"comment_count"`
	LikedByMe    bool     `json:"liked_by_me"`
	CanDelete    bool     `json:"can_delete"`
}

func (f *communityFixture) list(t *testing.T, sid string) []postItem {
	t.Helper()
	_, env := f.call(http.MethodGet, "/api/recommend?page=1&page_size=20", "", sid)
	if env.St != StatusOK {
		t.Fatalf("list st=%d msg=%s", env.St, env.Msg)
	}
	var data struct {
		Items     []postItem `json:"items"`
		Total     int        `json:"total"`
		PageSize  int        `json:"page_size"`
		HasMore   bool       `json:"has_more"`
		Moderator bool       `json:"is_moderator"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("list data: %v", err)
	}
	return data.Items
}

// TestCommunityLegacyDataStaysReadable locks in the compatibility promise: a
// document written by the previous release (no images / likes / comments) is
// listed and stays interactive.
func TestCommunityLegacyDataStaysReadable(t *testing.T) {
	f := newCommunityFixture(t)
	legacy := `{"v":1,"posts":[{"id":"legacy000000000000000001","comic_id":"442310","comic_title":"旧笔记",
		"kind":"comic","source":"jm","body":"旧数据","author":"alice","created_at":1700000000000}]}`
	seedPosts(t, legacy)

	sid := f.sid(t, "alice")
	items := f.list(t, sid)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	it := items[0]
	if it.Body != "旧数据" || it.ComicTitle != "旧笔记" || it.Author != "alice" {
		t.Fatalf("legacy item lost fields: %#v", it)
	}
	if it.Images == nil || len(it.Images) != 0 || it.LikeCount != 0 || it.CommentCount != 0 || it.LikedByMe {
		t.Fatalf("legacy stats = images:%#v likes:%d comments:%d liked:%v", it.Images, it.LikeCount, it.CommentCount, it.LikedByMe)
	}
	if !it.CanDelete {
		t.Fatal("author should be able to delete their legacy post")
	}

	_, env := f.call(http.MethodPost, "/api/recommend/"+it.ID+"/like", "", sid)
	if env.St != StatusOK {
		t.Fatalf("like legacy post st=%d msg=%s", env.St, env.Msg)
	}
	if items := f.list(t, sid); items[0].LikeCount != 1 || !items[0].LikedByMe {
		t.Fatalf("after like: %#v", items[0])
	}
}

// TestCommunityPostLikeCommentLifecycle covers the whole board flow through HTTP.
func TestCommunityPostLikeCommentLifecycle(t *testing.T) {
	f := newCommunityFixture(t)
	alice, bob, mod := f.sid(t, "alice"), f.sid(t, "bob"), f.sid(t, "mod")

	// A plain status post with image links (no work reference, no upload).
	body := `{"body":"今天把攒的三本一口气看完了，爽。","images":["https://img.example.com/a.png","https://img.example.com/a.png"]}`
	_, env := f.call(http.MethodPost, "/api/recommend", body, alice)
	if env.St != StatusOK {
		t.Fatalf("create st=%d msg=%s", env.St, env.Msg)
	}
	var post postItem
	if err := json.Unmarshal(env.Data, &struct {
		Status string    `json:"status"`
		Post   *postItem `json:"post"`
	}{Post: &post}); err != nil {
		t.Fatalf("create data: %v", err)
	}
	if post.ComicID != "" || post.ComicTitle != "" {
		t.Fatalf("plain post = %#v, want no work", post)
	}
	if len(post.Images) != 1 || post.Images[0] != "https://img.example.com/a.png" {
		t.Fatalf("images = %#v, want de-duplicated single link", post.Images)
	}
	if !post.CanDelete {
		t.Fatal("author should be able to delete their own post")
	}
	if post.Author != "alice" {
		t.Fatalf("author = %q", post.Author)
	}

	// Input validation.
	for _, tc := range []struct{ name, body, want string }{
		{"empty", `{"body":"  "}`, "Missing content"},
		{"bad link", `{"body":"x","images":["javascript:alert(1)"]}`, "Invalid image link"},
		{"too many", `{"body":"x","images":["https://a.com/1","https://a.com/2","https://a.com/3","https://a.com/4","https://a.com/5","https://a.com/6","https://a.com/7","https://a.com/8","https://a.com/9","https://a.com/10"]}`, "Too many images"},
		{"bad comic", `{"body":"x","comic_id":"abc"}`, "Invalid comic_id"},
	} {
		_, env := f.call(http.MethodPost, "/api/recommend", tc.body, alice)
		if env.St == StatusOK || env.Msg != tc.want {
			t.Fatalf("%s: st=%d msg=%q, want %q", tc.name, env.St, env.Msg, tc.want)
		}
	}

	// Likes: toggle, per-viewer flag, login required.
	_, env = f.call(http.MethodPost, "/api/recommend/"+post.ID+"/like", "", alice)
	if env.St != StatusOK {
		t.Fatalf("like st=%d msg=%s", env.St, env.Msg)
	}
	var like struct {
		Liked bool `json:"liked"`
		Count int  `json:"like_count"`
	}
	_ = json.Unmarshal(env.Data, &like)
	if !like.Liked || like.Count != 1 {
		t.Fatalf("like = %#v, want liked/1", like)
	}
	_, env = f.call(http.MethodPost, "/api/recommend/"+post.ID+"/like", "", alice)
	_ = json.Unmarshal(env.Data, &like)
	if like.Liked || like.Count != 0 {
		t.Fatalf("unlike = %#v, want !liked/0", like)
	}
	_, env = f.call(http.MethodPost, "/api/recommend/"+post.ID+"/like", "", "")
	if env.St != StatusNotLogin {
		t.Fatalf("anonymous like st=%d, want %d", env.St, StatusNotLogin)
	}
	_, env = f.call(http.MethodPost, "/api/recommend/"+post.ID+"/like", "", bob)
	_ = json.Unmarshal(env.Data, &like)
	if !like.Liked || like.Count != 1 {
		t.Fatalf("second liker = %#v", like)
	}
	if items := f.list(t, bob); !items[0].LikedByMe || items[0].LikeCount != 1 {
		t.Fatalf("bob view = %#v", items[0])
	}
	if items := f.list(t, alice); items[0].LikedByMe {
		t.Fatalf("alice view should not be liked: %#v", items[0])
	}

	// Comments: public read, login to write, author/moderator delete.
	_, env = f.call(http.MethodGet, "/api/recommend/"+post.ID+"/comments", "", "")
	if env.St != StatusOK {
		t.Fatalf("comments st=%d msg=%s", env.St, env.Msg)
	}
	_, env = f.call(http.MethodPost, "/api/recommend/"+post.ID+"/comments", `{"body":"同感！"}`, bob)
	if env.St != StatusOK {
		t.Fatalf("comment st=%d msg=%s", env.St, env.Msg)
	}
	var c struct {
		ID         string `json:"id"`
		Body       string `json:"body"`
		AuthorName string `json:"author_name"`
		CanDelete  bool   `json:"can_delete"`
	}
	if err := json.Unmarshal(env.Data, &c); err != nil {
		t.Fatalf("comment data: %v", err)
	}
	if c.Body != "同感！" || !c.CanDelete || c.AuthorName == "" {
		t.Fatalf("comment = %#v", c)
	}
	_, env = f.call(http.MethodPost, "/api/recommend/"+post.ID+"/comments", `{"body":"   "}`, bob)
	if env.St == StatusOK {
		t.Fatal("empty comment should be refused")
	}

	_, env = f.call(http.MethodDelete, "/api/recommend/comments/"+c.ID, "", alice)
	if env.St == StatusOK {
		t.Fatal("a stranger must not delete someone else's comment")
	}
	_, env = f.call(http.MethodDelete, "/api/recommend/comments/"+c.ID, "", mod)
	if env.St != StatusOK {
		t.Fatalf("moderator delete comment st=%d msg=%s", env.St, env.Msg)
	}

	if items := f.list(t, bob); items[0].CommentCount != 0 {
		t.Fatalf("comment_count = %d, want 0 after delete", items[0].CommentCount)
	}

	// Post deletion: stranger refused, author allowed, moderator allowed.
	_, env = f.call(http.MethodDelete, "/api/recommend/"+post.ID, "", bob)
	if env.St == StatusOK {
		t.Fatal("a stranger must not delete someone else's post")
	}
	_, env = f.call(http.MethodPost, "/api/recommend/"+post.ID+"/comments", `{"body":"留个言"}`, bob)
	if env.St != StatusOK {
		t.Fatalf("comment again st=%d", env.St)
	}
	_, env = f.call(http.MethodDelete, "/api/recommend/"+post.ID, "", mod)
	if env.St != StatusOK {
		t.Fatalf("moderator delete post st=%d msg=%s", env.St, env.Msg)
	}
	if items := f.list(t, mod); len(items) != 0 {
		t.Fatalf("posts after delete = %#v", items)
	}
}

// TestCommunityPostValidation keeps the "recommend a work" shape honest. Both
// cases are rejected before any upstream lookup, so the test stays offline; the
// happy path for a real work id is covered by the live smoke test.
func TestCommunityPostValidation(t *testing.T) {
	f := newCommunityFixture(t)
	alice := f.sid(t, "alice")
	_, env := f.call(http.MethodPost, "/api/recommend", `{"comic_id":"abc","kind":"comic","source":"jm","body":"推荐"}`, alice)
	if env.St == StatusOK || !strings.Contains(env.Msg, "Invalid comic_id") {
		t.Fatalf("jm numeric-id check: st=%d msg=%s", env.St, env.Msg)
	}
	// A bika post must reject an id that is not a bika id.
	_, env = f.call(http.MethodPost, "/api/recommend", `{"comic_id":"44231","source":"bika","body":"推荐"}`, alice)
	if env.St == StatusOK {
		t.Fatal("a 5-char id must be rejected for bika, whose ids are 24 hex chars")
	}
	if items := f.list(t, alice); len(items) != 0 {
		t.Fatalf("no post should have been created: %#v", items)
	}
}
