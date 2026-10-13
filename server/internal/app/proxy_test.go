package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 这些测试直接驱动真实的 chapter_image 处理器：把图床域名指向本地 httptest 服务，
// 把代码里那个固定走「不校验证书」的客户端换成测试服务的客户端，从而在离线环境下
// 验证故障转移、并发合并与响应头。

// withImageHosts 把图床候选域名临时替换为给定的域名列表。
func withImageHosts(t *testing.T, hosts ...string) {
	t.Helper()
	prev := apiClient().ImageDomains()
	apiClient().SetImageDomains(hosts)
	t.Cleanup(func() { apiClient().SetImageDomains(prev) })
}

// withProxyClient 把代理用的 HTTP 客户端临时换成测试服务自带的客户端（信任自签证书）。
func withProxyClient(t *testing.T, c *http.Client) {
	t.Helper()
	prev := proxyClientNoVerify
	proxyClientNoVerify = c
	t.Cleanup(func() { proxyClientNoVerify = prev })
}

func hostOf(ts *httptest.Server) string {
	return strings.TrimPrefix(ts.URL, "https://")
}

// chapterImageRequest 发一个章节图请求；handler 需要 PathValue，所以过一层 mux。
func chapterImageRequest(t *testing.T, photoID, imageName, query string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/chapter_image/{photo_id}/{image_name}", handleChapterImage)
	url := fmt.Sprintf("/api/chapter_image/%s/%s", photoID, imageName)
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// 首个候选图床网络层直接报错（端口已关闭）时，必须继续试下一个域名，
// 而不是像修复前那样把 500 甩给用户。
func TestChapterImageFailsOverWhenFirstHostIsUnreachable(t *testing.T) {
	dead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadHost := hostOf(dead)
	dead.Close() // 关掉，连接会被立刻拒绝

	var hits atomic.Int64
	good := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("WEBP-BYTES"))
	}))
	defer good.Close()

	withProxyClient(t, good.Client())
	withImageHosts(t, deadHost, hostOf(good))

	rec := chapterImageRequest(t, "12345", "00001.webp", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("应故障转移到可用图床，实际状态码 %d", rec.Code)
	}
	if rec.Body.String() != "WEBP-BYTES" {
		t.Fatalf("返回内容不符: %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/webp" {
		t.Fatalf("Content-Type 应透传上游，实际 %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "10" {
		t.Fatalf("应给出 Content-Length，实际 %q", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("可用图床应只被请求一次，实际 %d 次", hits.Load())
	}
}

// 所有候选都失败时应如实报错，而不是假装成功。
func TestChapterImageReportsFailureWhenAllHostsFail(t *testing.T) {
	dead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadHost := hostOf(dead)
	dead.Close()

	withImageHosts(t, deadHost)
	rec := chapterImageRequest(t, "999", "00001.webp", "")
	if rec.Code == http.StatusOK {
		t.Fatalf("全部失败时不应返回 200")
	}
}

// 非 200 的上游响应也要继续换域名（这条路径以前是对的，加个回归保护）。
func TestChapterImageContinuesOnUpstreamError(t *testing.T) {
	broken := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer broken.Close()

	good := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("OK-BYTES"))
	}))
	defer good.Close()

	withProxyClient(t, good.Client())
	withImageHosts(t, hostOf(broken), hostOf(good))

	rec := chapterImageRequest(t, "12345", "00002.webp", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "OK-BYTES" {
		t.Fatalf("应跳过 403 的图床，实际 %d %q", rec.Code, rec.Body.String())
	}
}

// 「同一 URL 并发合并」：热门章节被几十人同时打开时，同一张图只应打一次上游。
func TestChapterImageCoalescesConcurrentRequests(t *testing.T) {
	var hits atomic.Int64
	release := make(chan struct{})
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write([]byte("SHARED-BYTES"))
	}))
	defer ts.Close()

	withProxyClient(t, ts.Client())
	withImageHosts(t, hostOf(ts))

	const n = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, n)
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rec := chapterImageRequest(t, "777", "00003.webp", "")
			codes[i], bodies[i] = rec.Code, rec.Body.String()
		}(i)
	}
	close(start)
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // 让其余请求进入等待
	close(release)
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Fatalf("并发同一张图只应抓一次上游，实际 %d 次", got)
	}
	for i := range codes {
		if codes[i] != http.StatusOK || bodies[i] != "SHARED-BYTES" {
			t.Fatalf("第 %d 个请求结果不符: %d %q", i, codes[i], bodies[i])
		}
	}
}

// ── 直接验证抓取与合并这两个基础函数 ────────────────────────────────────────

// fetchChapterImageRaw 在任意网络错误后都应继续尝试下一个候选域名。
func TestFetchChapterImageRawTriesAllCandidates(t *testing.T) {
	dead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadHost := hostOf(dead)
	dead.Close()

	good := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BODY"))
	}))
	defer good.Close()

	withProxyClient(t, good.Client())

	res := fetchChapterImageRaw(context.Background(), []string{deadHost, hostOf(good)}, "1", "00001.webp")
	if res.err != nil {
		t.Fatalf("应成功: %v", res.err)
	}
	if string(res.body) != "BODY" {
		t.Fatalf("内容不符: %q", res.body)
	}
	// 上游没给 Content-Type 时应自行探测。
	if res.ct == "" {
		t.Fatal("应给出探测到的 Content-Type")
	}
}

// 抓取失败时不能返回半截内容。
func TestFetchChapterImageRawReturnsErrorWithoutBody(t *testing.T) {
	dead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadHost := hostOf(dead)
	dead.Close()

	res := fetchChapterImageRaw(context.Background(), []string{deadHost}, "1", "00001.webp")
	if res.err == nil {
		t.Fatal("应返回错误")
	}
	if len(res.body) != 0 {
		t.Fatalf("失败时不应带回内容，实际 %d 字节", len(res.body))
	}
}

// 合并器：同一 key 的并发调用只执行一次 fetch，等待者拿到同一份结果。
func TestChapterImageFetchMergedSharesOneFetch(t *testing.T) {
	var calls atomic.Int64
	release := make(chan struct{})
	const n = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]imgFetchResult, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = chapterImageFetchMerged(context.Background(), "merge-key", func(ctx context.Context) imgFetchResult {
				calls.Add(1)
				<-release
				return imgFetchResult{body: []byte("X"), ct: "image/webp", status: 200}
			})
		}(i)
	}
	close(start)
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("只应执行一次 fetch，实际 %d 次", got)
	}
	for i, r := range results {
		if r.err != nil || string(r.body) != "X" {
			t.Fatalf("第 %d 个结果不符: %+v", i, r)
		}
	}
}

// 不同 key 之间不能互相串（共享同一份字节会串图，属于严重问题）。
func TestChapterImageFetchMergedSeparatesKeys(t *testing.T) {
	var calls atomic.Int64
	fetch := func(ctx context.Context) imgFetchResult {
		n := calls.Add(1)
		return imgFetchResult{body: []byte(fmt.Sprintf("body-%d", n)), status: 200}
	}
	a := chapterImageFetchMerged(context.Background(), "key-a", fetch)
	b := chapterImageFetchMerged(context.Background(), "key-b", fetch)
	if string(a.body) == string(b.body) {
		t.Fatalf("不同 key 的结果不应相同: %q / %q", a.body, b.body)
	}
	if calls.Load() != 2 {
		t.Fatalf("不同 key 应各抓一次，实际 %d 次", calls.Load())
	}
}

// 首位请求方断线时，等待者仍应拿到结果（leader 的上下文脱掉了取消信号）。
func TestChapterImageFetchMergedSurvivesLeaderCancel(t *testing.T) {
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	release := make(chan struct{})
	started := make(chan struct{})

	go func() {
		chapterImageFetchMerged(leaderCtx, "cancel-key", func(ctx context.Context) imgFetchResult {
			close(started)
			<-release
			return imgFetchResult{body: []byte("STILL-HERE"), status: 200}
		})
	}()
	<-started
	cancelLeader() // leader 的调用方断线

	done := make(chan imgFetchResult, 1)
	go func() {
		<-started
		done <- chapterImageFetchMerged(context.Background(), "cancel-key", func(ctx context.Context) imgFetchResult {
			t.Error("等待者不应自己再抓一次上游")
			return imgFetchResult{err: errors.New("unexpected")}
		})
	}()
	time.Sleep(30 * time.Millisecond)
	close(release)

	select {
	case res := <-done:
		if res.err != nil || string(res.body) != "STILL-HERE" {
			t.Fatalf("等待者应复用 leader 的结果，实际 %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待者超时")
	}
}

// 候选域名顺序：请求显式指定 domain 时应排在最前。
func TestChapterImageCandidatesPreferRequestDomain(t *testing.T) {
	got := chapterImageCandidates("cdn.example.test")
	if len(got) == 0 || got[0] != "cdn.example.test" {
		t.Fatalf("请求指定的域名应排首位，实际 %v", got)
	}
	// 明显不是域名（带端口 / 内网地址）的值应被忽略，避免被当成上游打出去。
	for _, bad := range []string{"localhost:8080", "127.0.0.1", "192.168.1.9"} {
		if got := chapterImageCandidates(bad); len(got) > 0 && got[0] == bad {
			t.Fatalf("%q 不应作为候选域名", bad)
		}
	}
}

// 缓存统计接口应当可读且形状稳定。
func TestCacheStatsEndpoint(t *testing.T) {
	mux := NewRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/cache/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"upstream_api", "hit_rate", "chapter_images_decoded"} {
		if !strings.Contains(body, want) {
			t.Fatalf("统计响应缺少 %q: %s", want, body)
		}
	}
}

// 预热应当是可调用且幂等的（失败只影响效果，不影响正确性）。
func TestWarmupUpstreamIsSafe(t *testing.T) {
	withImageHosts(t, "not-a-real-host.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	WarmupUpstream(ctx) // 不应 panic 或长时间阻塞
}

// 上游非 200 时应把状态码带回来，便于把真实的失败原因透给排障的人。
func TestFetchChapterImageRawReportsUpstreamStatus(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "teapot")
	}))
	defer ts.Close()
	withProxyClient(t, ts.Client())

	res := fetchChapterImageRaw(context.Background(), []string{hostOf(ts)}, "1", "00001.webp")
	if res.err == nil {
		t.Fatal("应返回错误")
	}
	if res.status != http.StatusTeapot {
		t.Fatalf("应记录上游状态码 418，实际 %d", res.status)
	}
}
