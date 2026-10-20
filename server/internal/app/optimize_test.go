package app

import (
	"bytes"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// TestAcceptsEncoding 校验 Accept-Encoding 解析。
// 老实现用 strings.Contains 判断，遇到 "gzip;q=0"（明确表示不接受）也会判成接受。
func TestAcceptsEncoding(t *testing.T) {
	cases := []struct {
		header string
		enc    string
		want   bool
	}{
		{"gzip", "gzip", true},
		{"gzip, deflate, br", "gzip", true},
		{"GZIP", "gzip", true},
		{" gzip ", "gzip", true},
		{"gzip;q=1.0", "gzip", true},
		{"gzip;q=0.001", "gzip", true},
		{"gzip;q=0", "gzip", false},
		{"gzip;q=0.0", "gzip", false},
		{"br;q=1.0, gzip;q=0", "gzip", false},
		{"br;q=1.0, gzip;q=0.5", "gzip", true},
		{"deflate", "gzip", false},
		{"x-gzip", "gzip", false},
		{"", "gzip", false},
		{"br", "gzip", false},
		{"gzip", "br", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		if c.header != "" {
			r.Header.Set("Accept-Encoding", c.header)
		}
		if got := acceptsEncoding(r, c.enc); got != c.want {
			t.Errorf("Accept-Encoding=%q enc=%q => %v, 期望 %v", c.header, c.enc, got, c.want)
		}
	}
}

// TestNormalizePageParam 分页参数必须归一化，否则用户可控的原始字符串
// 会直接变成缓存键，缓存表就能被撑到无限大。
func TestNormalizePageParam(t *testing.T) {
	cases := map[string]string{
		"":         "0",
		"0":        "0",
		"3":        "3",
		" 4 ":      "4",
		"007":      "7",
		"-1":       "0",
		"abc":      "0",
		"1e3":      "0",
		"1.5":      "0",
		"999999":   "100",
		"101":      "100",
		"100":      "100",
		"%20":      "0",
		"12/../34": "0",
	}
	for in, want := range cases {
		if got := normalizePageParam(in); got != want {
			t.Errorf("normalizePageParam(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestPromoteCacheBounded 缓存条目必须有上限，超大响应不入缓存。
func TestPromoteCacheBounded(t *testing.T) {
	promoteMu.Lock()
	promoteCache = map[string]promoteEntry{}
	promoteMu.Unlock()

	for i := 0; i < promoteCacheMax*4; i++ {
		promotePut("page-"+strconv.Itoa(i), []byte("body"))
	}
	promoteMu.Lock()
	n := len(promoteCache)
	promoteMu.Unlock()
	if n > promoteCacheMax {
		t.Fatalf("缓存条目数 = %d, 超过上限 %d", n, promoteCacheMax)
	}

	// 超大正文不入缓存
	promotePut("huge", make([]byte, promoteCacheMaxLen+1))
	if _, ok := promoteGet("huge"); ok {
		t.Fatal("超大响应不应进入缓存")
	}

	// 正常读写，且返回的是副本（外部改动不影响缓存）
	promotePut("k", []byte("hello"))
	got, ok := promoteGet("k")
	if !ok {
		t.Fatal("刚写入的条目应该能读到")
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("读到 %q, 期望 hello", got)
	}
	got[0] = 'X'
	again, _ := promoteGet("k")
	if !bytes.Equal(again, []byte("hello")) {
		t.Fatal("缓存返回的应该是副本，不能被外部改写")
	}
}

// TestRateLimiterBounded 限流表的键数必须有上界，且过期键会被清理。
func TestRateLimiterBounded(t *testing.T) {
	l := newRateLimiter(50 * time.Millisecond)
	for i := 0; i < rateLimiterMaxKeys+50; i++ {
		l.allow("ip-"+strconv.Itoa(i), 5)
	}
	l.mu.Lock()
	n := len(l.hits)
	l.mu.Unlock()
	if n > rateLimiterMaxKeys {
		t.Fatalf("限流表键数 = %d, 超过上限 %d", n, rateLimiterMaxKeys)
	}

	// 等窗口过去，再来一次请求触发清扫
	time.Sleep(80 * time.Millisecond)
	l.allow("fresh", 5)
	l.mu.Lock()
	n = len(l.hits)
	l.mu.Unlock()
	if n > rateLimiterMaxKeys {
		t.Fatalf("清扫后键数 = %d, 仍超过上限 %d", n, rateLimiterMaxKeys)
	}
}

// TestRateLimiterStillLimits 确认改动没有破坏限流语义。
func TestRateLimiterStillLimits(t *testing.T) {
	l := newRateLimiter(time.Minute)
	for i := 0; i < 3; i++ {
		if !l.allow("same", 3) {
			t.Fatalf("第 %d 次应该放行", i+1)
		}
	}
	if l.allow("same", 3) {
		t.Fatal("超过阈值后应该被限流")
	}
	if !l.allow("other", 3) {
		t.Fatal("不同 key 不该受影响")
	}
}

// TestV2DecodeBodyLimit v2 接口的请求体必须有上限，否则一个超大请求就能吃光内存。
func TestV2DecodeBodyLimit(t *testing.T) {
	big := bytes.Repeat([]byte("a"), v2MaxBody+1024)
	r := httptest.NewRequest("POST", "/api/v2/jm/auth/login", bytes.NewReader(big))
	rec := httptest.NewRecorder()
	var dst map[string]any
	if v2DecodeBody(rec, r, &dst) {
		t.Fatal("超大请求体应被拒绝")
	}
	if rec.Code != 422 {
		t.Fatalf("状态码 = %d, 期望 422", rec.Code)
	}

	// 正常大小的请求体不受影响
	ok := httptest.NewRequest("POST", "/api/v2/jm/auth/login", bytes.NewReader([]byte(`{"username":"a"}`)))
	rec = httptest.NewRecorder()
	if !v2DecodeBody(rec, ok, &dst) {
		t.Fatal("正常请求体不应被拒绝")
	}
}
