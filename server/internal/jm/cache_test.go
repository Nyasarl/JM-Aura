package jm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── 缓存键与 TTL 策略 ────────────────────────────────────────────────────────

func TestAPICacheTTLPolicy(t *testing.T) {
	cached := []string{"/categories", "/album", "/chapter", "/search", "/leaderboard", "/latest"}
	for _, p := range cached {
		if got := apiCacheTTL(http.MethodGet, p); got <= 0 {
			t.Errorf("GET %s 应当被缓存，实际 TTL=%v", p, got)
		}
	}

	// 身份相关、强实时以及写操作都不该进缓存。
	notCached := []string{"/favorite", "/watch_list", "/setting", "/forum", "/login", "/unknown_endpoint"}
	for _, p := range notCached {
		if got := apiCacheTTL(http.MethodGet, p); got != 0 {
			t.Errorf("GET %s 不该被缓存，实际 TTL=%v", p, got)
		}
	}
	for _, p := range cached {
		if got := apiCacheTTL(http.MethodPost, p); got != 0 {
			t.Errorf("POST %s 不该被缓存，实际 TTL=%v", p, got)
		}
	}
}

func TestAPICacheTTLEnvToggles(t *testing.T) {
	t.Setenv("JM_AURA_API_CACHE_TTL_SCALE", "0")
	if got := apiCacheTTL(http.MethodGet, "/album"); got != 0 {
		t.Errorf("缩放为 0 时应关闭缓存，实际 TTL=%v", got)
	}
	t.Setenv("JM_AURA_API_CACHE_TTL_SCALE", "2")
	if got := apiCacheTTL(http.MethodGet, "/album"); got != time.Hour {
		t.Errorf("缩放为 2 时 /album 应为 1h，实际 %v", got)
	}
	t.Setenv("JM_AURA_API_CACHE_TTL_SCALE", "不是数字")
	if got := apiCacheTTL(http.MethodGet, "/album"); got != 30*time.Minute {
		t.Errorf("非法缩放值应回落到默认值，实际 %v", got)
	}
}

func TestAPICacheEnabledEnv(t *testing.T) {
	t.Setenv("JM_AURA_API_CACHE", "")
	if !APICacheEnabled() {
		t.Fatal("默认应启用缓存")
	}
	for _, v := range []string{"off", "0", "false", "disabled"} {
		t.Setenv("JM_AURA_API_CACHE", v)
		if APICacheEnabled() {
			t.Errorf("%q 应关闭缓存", v)
		}
	}
}

// 游客（不带凭证）必须共享同一个缓存桶，否则 200 人在线时命中率归零；
// 不同登录用户之间必须隔离，避免把 A 的数据给 B。
func TestAPICacheKeySharesGuestsAndIsolatesUsers(t *testing.T) {
	q := url.Values{"id": {"123"}}

	guestA := apiCacheKey(http.MethodGet, "/album", q, nil)
	guestB := apiCacheKey(http.MethodGet, "/album", q, map[string]string{})
	if guestA != guestB {
		t.Fatalf("游客之间应共享缓存键: %q != %q", guestA, guestB)
	}

	userA := apiCacheKey(http.MethodGet, "/album", q, map[string]string{"remember_me": "aaa"})
	userB := apiCacheKey(http.MethodGet, "/album", q, map[string]string{"remember_me": "bbb"})
	if userA == userB {
		t.Fatal("不同登录用户不应共享缓存键")
	}
	if userA == guestA || userB == guestA {
		t.Fatal("登录用户不应与游客共享缓存键")
	}

	// query 的书写顺序不影响键。
	ordered := apiCacheKey(http.MethodGet, "/search", url.Values{"a": {"1"}, "b": {"2"}}, nil)
	shuffled := apiCacheKey(http.MethodGet, "/search", url.Values{"b": {"2"}, "a": {"1"}}, nil)
	if ordered != shuffled {
		t.Fatalf("query 顺序不应影响缓存键: %q != %q", ordered, shuffled)
	}

	// 指纹不应还原出原始 cookie 值。
	fp := cookieFingerprint(map[string]string{"remember_me": "super-secret-token"})
	if strings.Contains(fp, "super-secret-token") {
		t.Fatal("指纹不应包含明文凭证")
	}
}

// ── 缓存本身的行为 ──────────────────────────────────────────────────────────

func TestAPICacheGetPutAndExpiry(t *testing.T) {
	c := newAPICache(1 << 20)
	base := time.Unix(1700000000, 0)
	now := base
	c.now = func() time.Time { return now }

	c.put("k", &CallResult{Data: []byte(`{"a":1}`)}, nil, time.Minute)

	got, err, ok := c.get("k")
	if !ok || err != nil || got == nil || string(got.Data) != `{"a":1}` {
		t.Fatalf("命中失败: ok=%v err=%v res=%+v", ok, err, got)
	}

	now = base.Add(2 * time.Minute)
	if _, _, ok := c.get("k"); ok {
		t.Fatal("过期条目不应命中")
	}
	if s := c.stats(); s.Expired == 0 {
		t.Fatal("应记录一次过期")
	}
}

func TestAPICacheNegativeEntriesAreServed(t *testing.T) {
	c := newAPICache(1 << 20)
	boom := errors.New("upstream down")
	c.put("k", nil, boom, apiCacheNegTTL)

	res, err, ok := c.get("k")
	if !ok || res != nil || !errors.Is(err, boom) {
		t.Fatalf("负缓存应原样返回错误: ok=%v res=%v err=%v", ok, res, err)
	}
}

func TestAPICacheZeroTTLIsNotStored(t *testing.T) {
	c := newAPICache(1 << 20)
	c.put("k", &CallResult{Data: []byte(`{}`)}, nil, 0)
	if _, _, ok := c.get("k"); ok {
		t.Fatal("TTL 为 0 时不应写入缓存")
	}
}

func TestAPICacheEvictsByBudget(t *testing.T) {
	const budget = 64 << 10
	c := newAPICache(budget)
	payload := make([]byte, 16<<10)
	for i := 0; i < 6; i++ {
		c.put(fmt.Sprintf("k%d", i), &CallResult{Data: payload}, nil, time.Hour)
	}
	s := c.stats()
	if s.Bytes > budget {
		t.Fatalf("占用超过预算: %d > %d", s.Bytes, budget)
	}
	if s.Evicted == 0 {
		t.Fatal("应发生逐出")
	}
	if s.Entries == 0 {
		t.Fatal("不应把缓存清空")
	}
}

func TestAPICacheRejectsOversizedEntry(t *testing.T) {
	c := newAPICache(1 << 20)
	c.put("big", &CallResult{Data: make([]byte, apiCacheEntryCap+1)}, nil, time.Hour)
	if _, _, ok := c.get("big"); ok {
		t.Fatal("超大单条不应进缓存")
	}
	if s := c.stats(); s.Rejected == 0 {
		t.Fatal("应记录一次拒绝")
	}
}

// ── 与上游交互的集成行为 ────────────────────────────────────────────────────

// newTestClient 造一个指向本地 httptest 服务的 jm.Client（域名列表被替换成该服务）。
func newTestClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	c := NewClient()
	c.http = ts.Client()
	c.apiDomains = []string{strings.TrimPrefix(ts.URL, "https://")}
	c.imageDomains = nil
	c.webDomains = nil
	c.domStats = map[string]domainStat{}
	c.SetAPICache(newAPICache(1 << 20))
	return c
}

func jsonEnvelope(data string) string {
	return `{"code":200,"data":` + data + `}`
}

// 未命中时打一次上游；随后的请求应完全命中缓存。
func TestAPICallServesRepeatedGetFromCache(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, jsonEnvelope(`{"album_id":"42"}`))
	}))
	defer ts.Close()
	c := newTestClient(t, ts)

	for i := 0; i < 3; i++ {
		res, err := c.APIGet(context.Background(), "/album", url.Values{"id": {"42"}}, nil)
		if err != nil {
			t.Fatalf("第 %d 次调用失败: %v", i, err)
		}
		if string(res.Data) != `{"album_id":"42"}` {
			t.Fatalf("返回内容不符: %s", res.Data)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("三次相同请求只应打一次上游，实际 %d 次", got)
	}
}

// 这就是「200 人同时打开同一章」的情形：并发请求必须合并成一次上游调用。
func TestAPICallCoalescesConcurrentRequests(t *testing.T) {
	var hits atomic.Int64
	release := make(chan struct{})
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-release // 卡住上游，确保所有并发请求都已经进入等待
		io.WriteString(w, jsonEnvelope(`{"ok":true}`))
	}))
	defer ts.Close()
	c := newTestClient(t, ts)

	const n = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = c.APIGet(context.Background(), "/chapter", url.Values{"id": {"7"}}, nil)
		}(i)
	}
	close(start)
	// 给所有 goroutine 一点时间进入 single-flight 等待。
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("第 %d 个并发请求失败: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("并发相同请求应合并成 1 次上游调用，实际 %d 次", got)
	}
	if s := c.cache.Load().stats(); s.Coalesced == 0 {
		t.Fatal("应记录被合并的请求数")
	}
}

func TestAPICallDoesNotCachePost(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, jsonEnvelope(`{"ok":true}`))
	}))
	defer ts.Close()
	c := newTestClient(t, ts)

	form := url.Values{"username": {"u"}}
	for i := 0; i < 3; i++ {
		if _, err := c.APIPost(context.Background(), "/login", form, nil); err != nil {
			t.Fatalf("登录请求失败: %v", err)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("POST 不应被缓存，应打 3 次上游，实际 %d 次", got)
	}
}

// 上游业务错误（如未登录）不应被缓存，否则修好之后还被"记住"。
func TestAPICallDoesNotCacheAPIError(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"code":1014,"errorMsg":"未登录"}`)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)

	for i := 0; i < 2; i++ {
		_, err := c.APIGet(context.Background(), "/album", url.Values{"id": {"1"}}, nil)
		var ae *APIError
		if !errors.As(err, &ae) {
			t.Fatalf("应返回 APIError，实际 %v", err)
		}
		if ae.Code != 1014 {
			t.Fatalf("错误码应为 1014，实际 %d", ae.Code)
		}
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("业务错误不应被缓存，应打 2 次上游，实际 %d 次", got)
	}
}

// 网络层失败应留一个很短的负缓存，避免上游抖动期间被反复打穿，
// 但绝不能是长 TTL。
func TestAPICallCachesNetworkErrorBriefly(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)

	for i := 0; i < 3; i++ {
		if _, err := c.APIGet(context.Background(), "/album", url.Values{"id": {"9"}}, nil); err == nil {
			t.Fatal("上游 502 时应当报错")
		}
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("故障期间应命中负缓存，只打 1 次上游，实际 %d 次", got)
	}
	if s := c.cache.Load().stats(); s.ErrorsCached == 0 {
		t.Fatal("应记录负缓存")
	}
}

// 关键修复：首个候选域名网络错误（连不上）时必须继续尝试后面的域名，
// 而不是直接把 500 甩给用户。
func TestAPICallFailsOverOnTransportError(t *testing.T) {
	// 一个已经关掉的端口，连接会立即被拒绝。
	dead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadHost := strings.TrimPrefix(dead.URL, "https://")
	dead.Close()

	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, jsonEnvelope(`{"from":"backup"}`))
	}))
	defer ts.Close()
	c := newTestClient(t, ts)

	c.apiDomains = []string{deadHost, strings.TrimPrefix(ts.URL, "https://")}
	res, err := c.APIGet(context.Background(), "/album", url.Values{"id": {"5"}}, nil)
	if err != nil {
		t.Fatalf("应故障转移到可用域名，实际失败: %v", err)
	}
	if string(res.Data) != `{"from":"backup"}` {
		t.Fatalf("返回内容不符: %s", res.Data)
	}
	if hits.Load() != 1 {
		t.Fatalf("备用域名应只被请求一次，实际 %d 次", hits.Load())
	}
	// 失败的域名应被降权，下次排到可用域名之后。
	if got := c.orderedAPIDomains(); got[0] != strings.TrimPrefix(ts.URL, "https://") {
		t.Fatalf("可用域名应排到首位，实际顺序 %v", got)
	}
}

// 域名排序应优先选实测延迟低的。
func TestOrderedAPIDomainsPrefersLowLatency(t *testing.T) {
	c := NewClient()
	c.domStats = map[string]domainStat{}
	c.apiDomains = []string{"slow.example", "new.example", "fast.example"}
	c.noteOK("slow.example", 3*time.Second)
	c.noteOK("fast.example", 150*time.Millisecond)

	got := c.orderedAPIDomains()
	want := []string{"fast.example", "new.example", "slow.example"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("排序不符: got %v want %v", got, want)
	}

	// 连续失败的域名应被继续降权，即使它曾经很快。
	c.noteFail("fast.example")
	c.noteFail("fast.example")
	c.noteFail("fast.example")
	if got := c.orderedAPIDomains(); got[0] != "new.example" {
		t.Fatalf("失败域名应让位，实际顺序 %v", got)
	}
}

// 恢复成功的域名应重新回到候选首位（失败计数清零）。
func TestDomainScoreRecoversAfterSuccess(t *testing.T) {
	c := NewClient()
	c.domStats = map[string]domainStat{}
	c.apiDomains = []string{"a.example", "b.example"}
	c.noteFail("a.example")
	c.noteFail("a.example")
	if c.orderedAPIDomains()[0] != "b.example" {
		t.Fatal("连续失败后 a 应让位")
	}
	c.noteOK("a.example", 100*time.Millisecond)
	if c.orderedAPIDomains()[0] != "a.example" {
		t.Fatal("a 恢复后应回到首位")
	}
}

// 关掉缓存后，每个请求都应回到"直连上游"的老行为。
func TestClientWithoutCacheAlwaysHitsUpstream(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, jsonEnvelope(`{"ok":true}`))
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	c.SetAPICache(nil)

	for i := 0; i < 3; i++ {
		if _, err := c.APIGet(context.Background(), "/album", url.Values{"id": {"1"}}, nil); err != nil {
			t.Fatalf("请求失败: %v", err)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("禁用缓存后应打 3 次上游，实际 %d 次", got)
	}
}

func TestDomainTimeoutEnvOverride(t *testing.T) {
	t.Setenv("JM_AURA_API_DOMAIN_TIMEOUT", "")
	if got := domainTimeout(); got != perDomainTimeout {
		t.Fatalf("默认应为 %v，实际 %v", perDomainTimeout, got)
	}
	t.Setenv("JM_AURA_API_DOMAIN_TIMEOUT", "3s")
	if got := domainTimeout(); got != 3*time.Second {
		t.Fatalf("环境变量应生效，实际 %v", got)
	}
	t.Setenv("JM_AURA_API_DOMAIN_TIMEOUT", "2500")
	if got := domainTimeout(); got != perDomainTimeout {
		t.Fatalf("纯数字（无单位）应视为非法并回落默认，实际 %v", got)
	}
	t.Setenv("JM_AURA_API_DOMAIN_TIMEOUT", "2500ms")
	if got := domainTimeout(); got != 2500*time.Millisecond {
		t.Fatalf("显式单位应生效，实际 %v", got)
	}
	t.Setenv("JM_AURA_API_DOMAIN_TIMEOUT", "-1")
	if got := domainTimeout(); got != perDomainTimeout {
		t.Fatalf("非法值应回落默认，实际 %v", got)
	}
}

// 并发压一压缓存本体：LRU 链表与字节计账在并发下必须保持自洽
// （本机装不了 gcc，跑不了 -race，所以这里用高频并发 + 不变量校验兜底）。
func TestAPICacheConcurrentStress(t *testing.T) {
	const budget = 256 << 10
	c := newAPICache(budget)
	payload := make([]byte, 4<<10)

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				key := fmt.Sprintf("k%d", (i+w)%64)
				c.put(key, &CallResult{Data: payload}, nil, time.Hour)
				c.get(key)
			}
		}(w)
	}
	// 同一条 key 的并发 single-flight 也不能出现重复 leader 或卡死。
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("flight-%d", i%4)
				fl, leader := c.begin(key)
				if leader {
					c.finish(key, fl, &CallResult{Data: []byte(`{}`)}, nil, time.Minute)
				} else {
					if _, err := fl.wait(context.Background()); err != nil {
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()

	c.mu.Lock()
	entries, listLen, bytes := len(c.entries), c.lru.Len(), c.curBytes
	c.mu.Unlock()
	if entries != listLen {
		t.Fatalf("map 与链表长度不一致: entries=%d list=%d", entries, listLen)
	}
	if bytes > budget {
		t.Fatalf("占用超出预算: %d > %d", bytes, budget)
	}
	if bytes < 0 {
		t.Fatalf("字节计账为负: %d", bytes)
	}
}

// JM_AURA_API_CACHE=off 必须真的让新建的 client 不带缓存。
// （这个开关曾经只是"文档里写了"，实际没接线——端到端冒烟才暴露出来。）
func TestNewClientRespectsCacheSwitch(t *testing.T) {
	t.Setenv("JM_AURA_API_CACHE", "")
	if !NewClient().CacheEnabled() {
		t.Fatal("默认应启用缓存")
	}
	for _, v := range []string{"off", "0", "false"} {
		t.Setenv("JM_AURA_API_CACHE", v)
		if NewClient().CacheEnabled() {
			t.Fatalf("JM_AURA_API_CACHE=%s 时不应启用缓存", v)
		}
		if s := SharedAPICacheStats(); s.Enabled {
			t.Fatalf("JM_AURA_API_CACHE=%s 时统计应报告 enabled=false", v)
		}
	}
}

// 关掉缓存后必须每次都打上游（而不是命中共享缓存）。
func TestCacheSwitchOffHitsUpstreamEveryTime(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, jsonEnvelope(`{"ok":true}`))
	}))
	defer ts.Close()

	t.Setenv("JM_AURA_API_CACHE", "off")
	c := newTestClient(t, ts)
	c.SetAPICache(defaultAPICache()) // 模拟"开机时该开关就是关的"
	if c.CacheEnabled() {
		t.Fatal("开关关闭时不应有缓存")
	}

	for i := 0; i < 3; i++ {
		if _, err := c.APIGet(context.Background(), "/album", url.Values{"id": {"1"}}, nil); err != nil {
			t.Fatalf("请求失败: %v", err)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("关闭缓存后应打 3 次上游，实际 %d 次", got)
	}
}
