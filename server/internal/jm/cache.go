package jm

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 上游只读接口的响应缓存。
//
// 动机：JM 的 API 本身是整个链路里最慢、最抖的一段（实测 /search 冷启动 ~2.5s、
// /album 1.2s~6.2s、/chapter ~0.9s），而阅读站里同一本热门漫画会被几十上百人
// 同时打开。旧实现对每个请求都现场打一次上游、现场解密，于是把上游的延迟和抖动
// 原样乘以在线人数放大给所有用户，同时把本机带宽和连接数消耗在完全重复的劳动上。
//
// 这里做的事：
//   - 只缓存 GET 只读接口（分类/详情/章节/搜索/榜单…）解密后的原始 JSON；
//   - 缓存键 = method + path + 规范化 query + 凭证指纹，所以登录用户与游客、
//     不同用户之间不会串数据；而游客不带 cookie，天然共享同一个桶（命中率最高）；
//   - 命中直接返回，不再打上游；
//   - 未命中时用 single-flight 合并，瞬间到来的 N 个相同请求只打上游一次；
//   - 上游报错时留一个很短的负缓存，避免故障期间把上游打穿；
//   - 写入 / 点赞 / 收藏 / 登录等 POST 或身份相关接口一律不缓存。

const (
	// apiCacheDefaultBytes 是缓存体量上限；值存的是解密后的 JSON，通常每条几 KB。
	apiCacheDefaultBytes = 64 << 20
	// apiCacheEntryCap 限制单条记录大小，避免一个大响应把整个缓存挤空。
	apiCacheEntryCap = 8 << 20
	// apiCacheNegTTL 是上游失败后的负缓存时长。
	apiCacheNegTTL = 5 * time.Second
	// apiCacheFillTimeout 是"填缓存"这个动作自己的时间上限（与调用方解耦）。
	apiCacheFillTimeout = 14 * time.Second
)

// apiCacheTTL 返回某上游只读接口的缓存时长；0 表示不缓存。
//
// 取值思路：内容越"死"缓存越久，越"活"越短。搜索要求新鲜，主要靠 single-flight
// 合并瞬时并发；章节图片清单和置乱参数对某一章是恒定值，可以放很久。
func apiCacheTTL(method, path string) time.Duration {
	if method != http.MethodGet {
		return 0
	}
	var d time.Duration
	switch path {
	case "/categories", "/categories/filter":
		d = 24 * time.Hour
	case "/chapter", "/novelchapters":
		d = 2 * time.Hour
	case "/album", "/comic", "/novel":
		d = 30 * time.Minute
	case "/promote", "/promoted", "/blogs", "/blog":
		d = 30 * time.Minute
	case "/novels":
		d = 15 * time.Minute
	case "/leaderboard", "/leaderboard2", "/daily", "/week", "/week/filter":
		d = 10 * time.Minute
	case "/search", "/search_novels":
		d = time.Minute
	case "/latest":
		d = 30 * time.Second
	default:
		// 收藏、观看记录、设置、论坛等身份相关或强实时接口：不缓存。
		return 0
	}
	return scaleTTL(d)
}

// scaleTTL 允许用 JM_AURA_API_CACHE_TTL_SCALE（浮点倍数，默认 1）整体缩放 TTL，
// 便于线上调参而不用改代码；设为 0 或负数等于关掉缓存。
// 值非法时忽略它并保留默认 TTL——不能因为一个笔误就把缓存悄悄关掉。
func scaleTTL(d time.Duration) time.Duration {
	s := strings.TrimSpace(os.Getenv("JM_AURA_API_CACHE_TTL_SCALE"))
	if s == "" {
		return d
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return d
	}
	if f <= 0 {
		return 0
	}
	return time.Duration(float64(d) * f)
}

// APICacheEnabled 报告上游响应缓存是否启用（JM_AURA_API_CACHE=off/0/false 可关闭）。
func APICacheEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("JM_AURA_API_CACHE"))) {
	case "off", "0", "false", "no", "disable", "disabled":
		return false
	}
	return true
}

func apiCacheBudget() int64 {
	s := strings.TrimSpace(os.Getenv("JM_AURA_API_CACHE_BYTES"))
	if s == "" {
		return apiCacheDefaultBytes
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return apiCacheDefaultBytes
	}
	return n
}

// CallResult 是可被多个并发读者共享的（Data 只读，Header 只读）。
type apiCacheEntry struct {
	key     string
	res     *CallResult
	err     error
	expires time.Time
	size    int64
}

type apiFlight struct {
	done chan struct{}
	res  *CallResult
	err  error
}

type apiCache struct {
	mu       sync.Mutex
	entries  map[string]*list.Element // key -> element(*apiCacheEntry)，list 头部最新
	lru      *list.List
	maxBytes int64
	curBytes int64
	now      func() time.Time

	inflightMu sync.Mutex
	inflight   map[string]*apiFlight

	// 统计（只增不减，供 /api/v2/cache-stats 观察）
	hits      atomic.Uint64
	misses    atomic.Uint64
	expired   atomic.Uint64
	coalesced atomic.Uint64
	evicted   atomic.Uint64
	rejected  atomic.Uint64
	errCached atomic.Uint64
}

func newAPICache(maxBytes int64) *apiCache {
	if maxBytes <= 0 {
		maxBytes = apiCacheDefaultBytes
	}
	return &apiCache{
		entries:  map[string]*list.Element{},
		lru:      list.New(),
		maxBytes: maxBytes,
		now:      time.Now,
		inflight: map[string]*apiFlight{},
	}
}

// sharedAPICache 是所有 jm.Client 共用的进程级缓存（登录等临时 client 也复用它）。
var sharedAPICache = newAPICache(apiCacheBudget())

// upstreamCalls 统计真正打出去的上游 API 调用次数（含未缓存的接口）。
// 有了它，监控里就不必再用「misses - coalesced」去猜真实上游压力。
var upstreamCalls atomic.Uint64

// UpstreamAPICalls 返回进程启动以来真实打到上游 API 的调用次数。
func UpstreamAPICalls() uint64 { return upstreamCalls.Load() }

// APICacheStats 是缓存统计快照。
type APICacheStats struct {
	Enabled       bool    `json:"enabled"`
	Entries       int     `json:"entries"`
	Bytes         int64   `json:"bytes"`
	MaxBytes      int64   `json:"max_bytes"`
	Hits          uint64  `json:"hits"`
	Misses        uint64  `json:"misses"`
	UpstreamCalls uint64  `json:"upstream_calls"`
	Expired       uint64  `json:"expired"`
	Coalesced     uint64  `json:"coalesced"`
	Evicted       uint64  `json:"evicted"`
	Rejected      uint64  `json:"rejected_too_large"`
	ErrorsCached  uint64  `json:"errors_cached"`
	HitRate       float64 `json:"hit_rate"`
}

// defaultAPICache 返回给新 client 使用的共享缓存；JM_AURA_API_CACHE=off 时返回 nil，
// 也就是完全不做缓存（老行为），便于线上排障与 A/B 对比。
func defaultAPICache() *apiCache {
	if !APICacheEnabled() {
		return nil
	}
	return sharedAPICache
}

// SharedAPICacheStats 返回共享缓存的统计快照。
func SharedAPICacheStats() APICacheStats {
	s := sharedAPICache.stats()
	s.Enabled = APICacheEnabled()
	return s
}

func (c *apiCache) stats() APICacheStats {
	if c == nil {
		return APICacheStats{}
	}
	c.mu.Lock()
	entries, bytes, maxBytes := len(c.entries), c.curBytes, c.maxBytes
	c.mu.Unlock()
	hits, misses := c.hits.Load(), c.misses.Load()
	var rate float64
	if total := hits + misses; total > 0 {
		rate = float64(hits) / float64(total)
	}
	return APICacheStats{
		Enabled:       true,
		Entries:       entries,
		Bytes:         bytes,
		MaxBytes:      maxBytes,
		Hits:          hits,
		Misses:        misses,
		UpstreamCalls: UpstreamAPICalls(),
		Expired:       c.expired.Load(),
		Coalesced:     c.coalesced.Load(),
		Evicted:       c.evicted.Load(),
		Rejected:      c.rejected.Load(),
		ErrorsCached:  c.errCached.Load(),
		HitRate:       rate,
	}
}

// get 返回命中的记录。ok 为 true 表示记录存在且未过期（res 与 err 至少一个非 nil）。
func (c *apiCache) get(key string) (*CallResult, error, bool) {
	if c == nil {
		return nil, nil, false
	}
	c.mu.Lock()
	el, ok := c.entries[key]
	if !ok {
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, nil, false
	}
	e := el.Value.(*apiCacheEntry)
	if !c.now().Before(e.expires) {
		c.removeLocked(el)
		c.mu.Unlock()
		c.expired.Add(1)
		c.misses.Add(1)
		return nil, nil, false
	}
	c.lru.MoveToFront(el)
	c.mu.Unlock()
	c.hits.Add(1)
	return e.res, e.err, true
}

func (c *apiCache) put(key string, res *CallResult, err error, ttl time.Duration) {
	if c == nil || ttl <= 0 {
		return
	}
	var size int64
	if res != nil {
		size = int64(len(res.Data)) + 128
	} else {
		size = 128
	}
	if size > apiCacheEntryCap || size > c.maxBytes/2 {
		c.rejected.Add(1)
		return
	}
	entry := &apiCacheEntry{
		key:     key,
		res:     cacheableView(res),
		err:     err,
		expires: c.now().Add(ttl),
		size:    size,
	}
	if err != nil {
		c.errCached.Add(1)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		prev := old.Value.(*apiCacheEntry)
		c.curBytes -= prev.size
		old.Value = entry
		c.lru.MoveToFront(old)
	} else {
		el := c.lru.PushFront(entry)
		c.entries[key] = el
	}
	c.curBytes += entry.size
	for c.curBytes > c.maxBytes && c.lru.Len() > 1 {
		last := c.lru.Back()
		if last == nil {
			break
		}
		c.removeLocked(last)
		c.evicted.Add(1)
	}
}

// cacheableView 只保留缓存需要的内容：解密后的 JSON 与响应头。Raw（base64 原文）
// 体积大且只有排障时才用得上，不进缓存。
func cacheableView(res *CallResult) *CallResult {
	if res == nil {
		return nil
	}
	return &CallResult{Data: res.Data, Header: res.Header}
}

func (c *apiCache) removeLocked(el *list.Element) {
	e, _ := el.Value.(*apiCacheEntry)
	c.lru.Remove(el)
	if e != nil {
		delete(c.entries, e.key)
		c.curBytes -= e.size
		if c.curBytes < 0 {
			c.curBytes = 0
		}
	}
}

// reset 清空缓存（测试用）。
func (c *apiCache) reset() {
	c.mu.Lock()
	c.entries = map[string]*list.Element{}
	c.lru.Init()
	c.curBytes = 0
	c.mu.Unlock()
}

// begin 开始一次 single-flight：leader 为 true 表示由调用方去打上游，
// 否则应等待返回的 flight.done 复用 leader 的结果。
func (c *apiCache) begin(key string) (*apiFlight, bool) {
	c.inflightMu.Lock()
	defer c.inflightMu.Unlock()
	if f, ok := c.inflight[key]; ok {
		c.coalesced.Add(1)
		return f, false
	}
	f := &apiFlight{done: make(chan struct{})}
	c.inflight[key] = f
	return f, true
}

func (c *apiCache) finish(key string, f *apiFlight, res *CallResult, err error, ttl time.Duration) {
	// 先写缓存、再唤醒等待者，保证它们醒来时看到的是同一份结果。
	c.put(key, res, err, ttl)
	f.res, f.err = res, err
	close(f.done)
	// 放在最后删除：在 close 与 delete 之间到达的请求会直接复用这个刚完成的结果，
	// 而不是又开一次上游调用。
	c.inflightMu.Lock()
	if cur, ok := c.inflight[key]; ok && cur == f {
		delete(c.inflight, key)
	}
	c.inflightMu.Unlock()
}

// wait 让非 leader 等待 leader 的结果。
func (f *apiFlight) wait(ctx context.Context) (*CallResult, error) {
	if f == nil {
		return nil, errors.New("jm: nil flight")
	}
	select {
	case <-f.done:
		return f.res, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// apiCacheKey 构造缓存键。query 会被 url.Values.Encode 规范化（按 key 排序），
// 因此同一组参数的不同书写顺序命中同一条缓存。
func apiCacheKey(method, path string, query url.Values, cookies map[string]string) string {
	var b strings.Builder
	b.Grow(len(path) + 32)
	b.WriteString(method)
	b.WriteByte(' ')
	b.WriteString(path)
	if len(query) > 0 {
		b.WriteByte('?')
		b.WriteString(query.Encode())
	}
	if fp := cookieFingerprint(cookies); fp != "" {
		b.WriteByte('#')
		b.WriteString(fp)
	}
	return b.String()
}

// cookieFingerprint 返回请求凭证的稳定摘要，空/无凭证时返回 ""。
//
// 关键点：游客（以及未登录的只读访问）不带 cookie，全部映射到同一个桶，
// 于是 200 个在线用户共享同一份上游数据；而带登录态的请求按用户隔离，
// 不会把 A 的数据给 B。
func cookieFingerprint(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	keys := make([]string, 0, len(cookies))
	for k := range cookies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(cookies[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}
