package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"jmaura/internal/bika"
	"jmaura/internal/provider"
)

// withBikaUpstream 把全局 bikaClient 指向测试服务器，测试结束恢复。
// bikaPublicToken 是包级缓存，必须一起清掉，否则第二个用例会复用上一个用例的 token。
func withBikaUpstream(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewServer(h)
	oldClient := bikaClient
	oldToken := bikaPublicToken
	bikaClient = bika.NewClient()
	bikaClient.SetBaseURLs([]string{ts.URL + "/"})
	bikaPublicToken = ""
	t.Cleanup(func() {
		bikaClient = oldClient
		bikaPublicToken = oldToken
		ts.Close()
	})
}

// testLeaderboardPayload 模拟上游 comics/leaderboard 的响应：data.comics[] 定长列表。
const testLeaderboardPayload = `{"code":200,"message":"ok","data":{"comics":[
 {"_id":"c1","title":"本子一","author":"作者甲","totalViews":123456,"likesCount":888,
  "pagesCount":30,"epsCount":3,"finished":true,"categories":["同人"],"tags":["t1"],
  "thumb":{"fileServer":"https://img.example/","path":"1.jpg"}},
 {"_id":"c2","title":"本子二","author":"作者乙","totalViews":50,"totalLikes":7,
  "categories":[],"tags":[],"thumb":{"fileServer":"https://img.example/","path":"2.jpg"}}
]}}`

func TestBikaLeaderboardPeriod(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "H24", false},   // 不传默认 24 小时
		{"H24", "H24", false},
		{"D7", "D7", false},
		{"d30", "D30", false}, // 大小写不敏感
		{" D7 ", "D7", false},
		{"30d", "", true}, // 非法值必须报错，不能悄悄退回默认
	}
	for _, c := range cases {
		got, err := bikaLeaderboardPeriod(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("bikaLeaderboardPeriod(%q) 期望报错，实际得到 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("bikaLeaderboardPeriod(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("bikaLeaderboardPeriod(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestBikaLeaderboardProvider 端到端验证：登录 → 榜单请求参数 → 字段映射 → 缓存命中。
func TestBikaLeaderboardProvider(t *testing.T) {
	var hits int
	var gotTT, gotCT string

	withBikaUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/sign-in":
			_, _ = io.WriteString(w, `{"code":200,"message":"ok","data":{"token":"tok-1"}}`)
		case "/comics/leaderboard":
			hits++
			gotTT = r.URL.Query().Get("tt")
			gotCT = r.URL.Query().Get("ct")
			_, _ = io.WriteString(w, testLeaderboardPayload)
		default:
			t.Errorf("未预期的上游请求: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	t.Setenv("BIKA_ACCOUNT", "pub@example.com")
	t.Setenv("BIKA_PASSWORD", "secret")

	p := bikaProvider{}
	ctx := context.Background()
	q := provider.LeaderboardQuery{Category: "0", Page: 3, Sort: "mv", Period: "D7"}

	raw, err := p.Leaderboard(ctx, "", q)
	if err != nil {
		t.Fatalf("Leaderboard 失败: %v", err)
	}
	items, ok := raw.([]v2ComicSummary)
	if !ok {
		t.Fatalf("期望 []v2ComicSummary，实际 %T", raw)
	}

	// 1. 请求参数：时间段透传、榜单类型固定 VC，page/sort 不应出现在榜单请求里。
	if gotTT != "D7" {
		t.Errorf("tt = %q，期望 D7", gotTT)
	}
	if gotCT != "VC" {
		t.Errorf("ct = %q，期望 VC", gotCT)
	}

	// 2. 字段映射
	if len(items) != 2 {
		t.Fatalf("条目数 = %d，期望 2", len(items))
	}
	if items[0].ComicID != "c1" || items[0].Title != "本子一" {
		t.Errorf("首条 id/title 映射错误: %+v", items[0])
	}
	if items[0].Author == nil || *items[0].Author != "作者甲" {
		t.Errorf("author 映射错误: %v", items[0].Author)
	}
	if items[0].Views == nil || *items[0].Views != 123456 {
		t.Errorf("totalViews 未映射到 Views: %v", items[0].Views)
	}
	if items[0].Likes == nil || *items[0].Likes != 888 {
		t.Errorf("likesCount 未映射到 Likes: %v", items[0].Likes)
	}
	// 没有 likesCount 时回退 totalLikes
	if items[1].Likes == nil || *items[1].Likes != 7 {
		t.Errorf("totalLikes 回退失败: %v", items[1].Likes)
	}
	if items[0].Source != bikaSource {
		t.Errorf("source = %q，期望 %q", items[0].Source, bikaSource)
	}

	// 3. 缓存：同参数第二次不应该再打上游
	if _, err := p.Leaderboard(ctx, "", q); err != nil {
		t.Fatalf("第二次 Leaderboard 失败: %v", err)
	}
	if hits != 1 {
		t.Errorf("上游被请求 %d 次，期望 1 次（10 分钟缓存内应复用）", hits)
	}

	// 4. 缓存必须返回副本：handler 里的 v2MaskList 会原地压缩切片，
	//    如果返回共享底层数组，屏蔽规则会把缓存内容改坏。
	again, err := p.Leaderboard(ctx, "", q)
	if err != nil {
		t.Fatalf("第三次 Leaderboard 失败: %v", err)
	}
	againItems := again.([]v2ComicSummary)
	againItems[0].Title = "被前端改坏"
	after, err := p.Leaderboard(ctx, "", q)
	if err != nil {
		t.Fatalf("第四次 Leaderboard 失败: %v", err)
	}
	if got := after.([]v2ComicSummary)[0].Title; got != "本子一" {
		t.Errorf("缓存被调用方改写: title = %q", got)
	}

	// 5. 不同时间段不应共用缓存
	gotTT = ""
	if _, err := p.Leaderboard(ctx, "", provider.LeaderboardQuery{Period: "D30"}); err != nil {
		t.Fatalf("D30 Leaderboard 失败: %v", err)
	}
	if gotTT != "D30" {
		t.Errorf("换时间段后 tt = %q，期望 D30（说明缓存键没区分时间段）", gotTT)
	}
}

// TestBikaLeaderboardInvalidPeriod 非法时间段应返回可读错误，而不是静默给默认榜单。
func TestBikaLeaderboardInvalidPeriod(t *testing.T) {
	p := bikaProvider{}
	_, err := p.Leaderboard(context.Background(), "", provider.LeaderboardQuery{Period: "WEEK"})
	if err == nil {
		t.Fatal("非法时间段应当报错")
	}
	if pe, ok := err.(*v2ProviderError); !ok || pe.status != 400 {
		t.Errorf("期望 400 的 v2ProviderError，实际 %#v", err)
	}
}