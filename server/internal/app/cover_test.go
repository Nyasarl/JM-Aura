package app

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestFillCoverImages 上游 /promote 的 image 字段常常是空的。
// 服务端补齐地址后：域名只此一处、用的是省流量的小变体、
// 预热用的 URL 与前端请求的 URL 一致。
func TestFillCoverImages(t *testing.T) {
	data := []any{
		map[string]any{
			"id":      "123",
			"content": []any{},
		},
		map[string]any{
			"id": 456,
			"content": []any{
				map[string]any{"id": "1454539", "image": ""},
				map[string]any{"id": "77", "image": "/media/novels/77.jpg"},
				map[string]any{"id": "88", "image": "/media/library/88.jpg"},
				map[string]any{"id": "99", "image": "https://other.cdn/x.jpg"},
				map[string]any{"image": ""}, // 没有 id，跳过
			},
		},
	}
	items := fillCoverImages(data)
	secs := data[1].(map[string]any)
	content := secs["content"].([]any)

	// 空 image 用作品 id 拼地址
	got := content[0].(map[string]any)["image"].(string)
	if !strings.HasPrefix(got, "https://") || !strings.HasSuffix(got, "/media/albums/1454539.jpg") {
		t.Fatalf("空 image 未补齐: %q", got)
	}
	// 相对路径补成绝对地址
	got = content[1].(map[string]any)["image"].(string)
	if !strings.HasPrefix(got, "https://") || !strings.HasSuffix(got, "/media/novels/77.jpg") {
		t.Fatalf("相对路径未补成绝对地址: %q", got)
	}
	// 书库路径保持相对：前端据此显示占位图
	if got = content[2].(map[string]any)["image"].(string); got != "/media/library/88.jpg" {
		t.Fatalf("书库路径被改写了: %q", got)
	}
	// 已经是绝对地址的不动
	if got = content[3].(map[string]any)["image"].(string); got != "https://other.cdn/x.jpg" {
		t.Fatalf("绝对地址被改写了: %q", got)
	}
	// 预热清单：书库路径与无 id 的条目不进预热
	if len(items) != 2 {
		t.Fatalf("预热清单长度 = %d, 期望 2 (%+v)", len(items), items)
	}
	for _, it := range items {
		if it.Width != 320 {
			t.Fatalf("预热宽度 = %d, 期望 320", it.Width)
		}
		if !strings.HasPrefix(it.URL, "https://") {
			t.Fatalf("预热 URL 不是绝对地址: %q", it.URL)
		}
	}
}

// TestFillCoverImagesNumericID id 是数字时也要能拼出地址。
func TestFillCoverImagesNumericID(t *testing.T) {
	data := []any{map[string]any{"content": []any{map[string]any{"id": float64(42), "image": ""}}}}
	items := fillCoverImages(data)
	if len(items) != 1 || !strings.HasSuffix(items[0].URL, "/media/albums/42.jpg") {
		t.Fatalf("数字 id 未拼出地址: %+v", items)
	}
}

// TestCoverCandidates 图床域名之间要能互相回退；非图床域名不能乱换。
func TestCoverCandidates(t *testing.T) {
	u, _ := url.Parse("https://cdn-msp.jmapiproxy2.cc/media/albums/1.jpg")
	cands := coverCandidates(u, 320)
	if len(cands) < 2 {
		t.Fatalf("图床域名应生成回退候选, 实际 %d 个: %v", len(cands), cands)
	}
	if cands[0] != u.String() {
		t.Fatalf("原地址应排第一, 实际 %q", cands[0])
	}
	for _, c := range cands[1:] {
		if !strings.Contains(c, "/media/albums/1.jpg") {
			t.Fatalf("回退候选路径必须保持不变: %q", c)
		}
	}

	// 第三方图源不允许换域名
	u2, _ := url.Parse("https://img.bika-cdn.com/cover.jpg")
	if cands := coverCandidates(u2, 320); len(cands) != 1 {
		t.Fatalf("第三方图源不应有回退候选, 实际 %v", cands)
	}
}

// TestCoverCandidatesOrder 已知宽度够用的域名应排在前面：
// 请求 320 宽的封面时先抓小变体，而不是先抓 600px 的大变体再缩。
func TestCoverCandidatesOrder(t *testing.T) {
	coverHostWidthMu.Lock()
	coverHostWidth["cdn-msp.jmapiproxy1.cc"] = 306 // 小变体
	coverHostWidth["cdn-msp.jmapiproxy2.cc"] = 600 // 大变体
	coverHostWidthMu.Unlock()
	defer func() {
		coverHostWidthMu.Lock()
		delete(coverHostWidth, "cdn-msp.jmapiproxy1.cc")
		delete(coverHostWidth, "cdn-msp.jmapiproxy2.cc")
		coverHostWidthMu.Unlock()
	}()

	u, _ := url.Parse("https://cdn-msp.jmapiproxy2.cc/media/albums/1.jpg")
	cands := coverCandidates(u, 320)
	idx := map[string]int{}
	for i, c := range cands {
		idx[c] = i
	}
	small := "https://cdn-msp.jmapiproxy1.cc/media/albums/1.jpg"
	big := "https://cdn-msp.jmapiproxy2.cc/media/albums/1.jpg"
	if idx[small] > idx[big] {
		t.Fatalf("小变体(306px)应排在 320 请求前面: %v", cands)
	}

	// 请求 480 宽时，小变体不够用，应排后面
	cands = coverCandidates(u, 480)
	idx = map[string]int{}
	for i, c := range cands {
		idx[c] = i
	}
	if idx[small] < idx[big] {
		t.Fatalf("480 请求时小变体(306px)应排在大变体后面: %v", cands)
	}
}

// TestEtagMatch If-None-Match 的各种写法。
func TestEtagMatch(t *testing.T) {
	etag := `"abc123"`
	cases := map[string]bool{
		`"abc123"`:           true,
		`"abc123", "def456"`: true,
		`"def456", "abc123"`: true,
		`*`:                  true,
		`"other"`:            false,
		``:                   false,
	}
	for header, want := range cases {
		if got := etagMatch(header, etag); got != want {
			t.Errorf("etagMatch(%q) = %v, 期望 %v", header, got, want)
		}
	}
}

// TestCoverETag 同内容同 ETag，不同内容不同 ETag。
func TestCoverETag(t *testing.T) {
	if coverETag([]byte("abc")) != coverETag([]byte("abc")) {
		t.Fatal("同内容应得到相同 ETag")
	}
	if coverETag([]byte("abc")) == coverETag([]byte("abd")) {
		t.Fatal("不同内容应得到不同 ETag")
	}
}

// TestImageProxyETagAndCache 走真实路由：第一次抓上游并缓存，
// 第二次带 If-None-Match 应回 304，且不再打上游。
func TestImageProxyETagAndCache(t *testing.T) {
	imageProxyAllowPrivateHosts = true
	defer func() { imageProxyAllowPrivateHosts = false }()
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(makeJPEG(t, 600, 600))
	}))
	defer upstream.Close()

	srv := httptest.NewServer(Wrap(NewRouter()))
	defer srv.Close()

	target := upstream.URL + "/media/albums/1.jpg"
	proxyURL := srv.URL + "/api/image-proxy?url=" + url.QueryEscape(target) + "&w=320"

	resp, err := http.Get(proxyURL)
	if err != nil {
		t.Fatalf("首次请求失败: %v", err)
	}
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("缺少 ETag")
	}
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("Cache-Control = %q, 应含 immutable", cc)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body[:n]))
	if err != nil {
		t.Fatalf("返回的不是可解码图片: %v", err)
	}
	if cfg.Width != 320 {
		t.Fatalf("缩略图宽度 = %d, 期望 320", cfg.Width)
	}

	// 条件请求应回 304
	req, _ := http.NewRequest("GET", proxyURL, nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("条件请求失败: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Fatalf("条件请求状态码 = %d, 期望 304", resp2.StatusCode)
	}

	// 缓存命中：不再打上游
	resp3, err := http.Get(proxyURL)
	if err != nil {
		t.Fatalf("二次请求失败: %v", err)
	}
	resp3.Body.Close()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("上游被请求 %d 次, 期望 1 次（第二次应命中缓存）", got)
	}
}

// makeJPEG 生成一张测试用 JPEG。
func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("生成 JPEG 失败: %v", err)
	}
	return buf.Bytes()
}

// TestImageProxyUpstreamHang 上游挂死时必须尽快放弃，而不是把请求一直吊着。
//
// 实测图床会随机把连接挂住（一次 24 并发里就有一个卡了 30 秒）。
// 没有超时的话，这条连接会长期占住额度槽位，队列越堆越长，
// 用户看到的就是「封面一直转圈不显示」。
func TestImageProxyUpstreamHang(t *testing.T) {
	imageProxyAllowPrivateHosts = true
	defer func() { imageProxyAllowPrivateHosts = false }()
	oldTimeout, oldHedge := coverCandidateTimeout, coverHedgeDelay
	coverCandidateTimeout = 300 * time.Millisecond
	coverHedgeDelay = 100 * time.Millisecond
	defer func() { coverCandidateTimeout, coverHedgeDelay = oldTimeout, oldHedge }()

	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // 永远不回应
	}))
	defer upstream.Close()
	defer close(release)

	srv := httptest.NewServer(Wrap(NewRouter()))
	defer srv.Close()
	target := upstream.URL + "/media/albums/1.jpg"
	proxyURL := srv.URL + "/api/image-proxy?url=" + url.QueryEscape(target) + "&w=320"

	start := time.Now()
	resp, err := http.Get(proxyURL)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 502", resp.StatusCode)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("上游挂死时耗时 %v, 太久（应该尽快放弃）", elapsed)
	}
}

// TestImageProxyKeepsSmallSource 源图不比目标大时应原样透传：
// 不重编码就等于省掉一次整幅解码 + 重编码（纯 Go 解 WebP 要 100~300ms CPU），
// 客户端拿到的还是原图（更清晰）。
func TestImageProxyKeepsSmallSource(t *testing.T) {
	imageProxyAllowPrivateHosts = true
	defer func() { imageProxyAllowPrivateHosts = false }()

	orig := makeJPEG(t, 100, 100)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(orig)
	}))
	defer upstream.Close()

	srv := httptest.NewServer(Wrap(NewRouter()))
	defer srv.Close()
	target := upstream.URL + "/media/albums/1.jpg"
	proxyURL := srv.URL + "/api/image-proxy?url=" + url.QueryEscape(target) + "&w=320"

	resp, err := http.Get(proxyURL)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", resp.StatusCode)
	}
	if !bytes.Equal(got, orig) {
		t.Fatalf("源图比目标小，应当原样透传：拿到 %d 字节，原始 %d 字节", len(got), len(orig))
	}
}
