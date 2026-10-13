package jm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultScrambleID = 220980
	requestTimeout    = 15 * time.Second
	// perDomainTimeout 是「单个上游域名一次尝试」的时间上限，可用
	// JM_AURA_API_DOMAIN_TIMEOUT 覆盖。上游本身偏慢（实测 /album 在冷连接下
	// 能到 6s+），所以默认留 8s 的余量；真正的整体上限由 requestTimeout 兜底。
	perDomainTimeout = 8 * time.Second
	// maxFailoverRounds 是一次调用里「换一批域名重来」的轮数。
	maxFailoverRounds = 2
	// failoverRaceWidth 是首选域名失败后并行抢跑的候选数量。原来是一个域名
	// 串行等 8s、失败再等下一个，最坏情况用户要挂在十几秒上；改成首选失败后
	// 让接下来两个候选同时抢跑，谁先成功用谁（正常路径不增加任何上游流量）。
	failoverRaceWidth = 2
)

// domainTimeout 返回单域名尝试超时（允许用环境变量在线调参）。
func domainTimeout() time.Duration {
	if d := envDuration("JM_AURA_API_DOMAIN_TIMEOUT"); d > 0 {
		return d
	}
	return perDomainTimeout
}

var DefaultWebDomains = []string{
	"https://18comic-hok.vip",
	"https://18comic.vip",
	"https://jmcomic.me",
	"https://18comic-16promax.club",
	"https://18comic.tw",
	"https://18comic-doa.xyz",
}

type CallResult struct {
	Data   json.RawMessage
	Raw    []byte
	Header http.Header
}

type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string { return fmt.Sprintf("jm api error %d: %s", e.Code, e.Msg) }

type Client struct {
	http *http.Client

	mu           sync.RWMutex
	apiDomains   []string
	imageDomains []string
	webDomains   []string
	lastOK       string
	// domStats 记录每个上游域名的健康度与实测延迟，用来给候选域名排序。
	domStats map[string]domainStat

	// cache 是上游只读接口的共享响应缓存，nil 表示不缓存。用原子指针存，
	// 这样运行期开关缓存也是并发安全的。
	cache atomic.Pointer[apiCache]
}

// domainStat 是某个上游域名的运行统计（EMA 延迟 + 连续失败数）。
type domainStat struct {
	latency  time.Duration
	sampled  bool
	failures int
}

func NewClient() *Client {
	c := &Client{
		http:         sharedAPIHTTPClient,
		apiDomains:   append([]string(nil), DefaultAPIDomains...),
		imageDomains: append([]string(nil), DefaultImageDomains...),
		webDomains:   append([]string(nil), DefaultWebDomains...),
		domStats:     map[string]domainStat{},
	}
	c.cache.Store(defaultAPICache())
	return c
}

// SetAPICache 关掉/替换响应缓存（nil 表示禁用）。主要供测试与线上排障使用。
func (c *Client) SetAPICache(cache *apiCache) { c.cache.Store(cache) }

// CacheEnabled 报告该 client 当前是否在使用上游响应缓存。
func (c *Client) CacheEnabled() bool { return c.cache.Load() != nil }

func copyList(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func (c *Client) APIDomains() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyList(c.apiDomains)
}

func (c *Client) ImageDomains() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyList(c.imageDomains)
}

func (c *Client) WebDomains() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return copyList(c.webDomains)
}

func (c *Client) SetAPIDomains(ds []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ds) > 0 {
		c.apiDomains = ds
	}
}

func (c *Client) SetImageDomains(ds []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ds) > 0 {
		c.imageDomains = ds
	}
}

// noteOK 记录一次成功：更新该域名的延迟 EMA 与失败计数，并把它排到候选首位。
func (c *Client) noteOK(d string, latency time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.domStats[d]
	if !st.sampled {
		st.latency = latency
		st.sampled = true
	} else {
		// EMA 系数 0.3：既跟随网络变化，又不会被单次抖动带偏。
		st.latency = time.Duration(float64(st.latency)*0.7 + float64(latency)*0.3)
	}
	st.failures = 0
	c.domStats[d] = st
	c.lastOK = "https://" + d
}

// noteFail 记录一次失败（网络错误、非 200、超时）：累计失败数，供排序降权。
func (c *Client) noteFail(d string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.domStats[d]
	if st.failures < 8 {
		st.failures++
	}
	st.sampled = true
	c.domStats[d] = st
}

func (c *Client) resetAPIDomains() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiDomains = append([]string(nil), DefaultAPIDomains...)
}

// orderedAPIDomains 返回按「实测表现」排序的候选域名：没失败过、延迟低的排前面。
//
// 之前只记录「最后一个成功的域名」并把它挪到队首，等于用一次成功覆盖所有信息；
// 现在用 EMA 延迟 + 连续失败数打分，既能让慢域名让位，也能让恢复的域名重新上任。
// 注意：任何排序都只影响尝试顺序，不会丢弃候选，最差情况退化成顺序重试。
func (c *Client) orderedAPIDomains() []string {
	return c.orderDomains(c.APIDomains())
}

// orderDomains 按域名实测表现给任意候选列表排序，返回新切片。
func (c *Client) orderDomains(list []string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := copyList(list)
	sort.SliceStable(out, func(i, j int) bool {
		return c.domainScoreLocked(out[i]) < c.domainScoreLocked(out[j])
	})
	return out
}

// domainScoreLocked 越小越优先。未探测过的域名给一个中间分，避免被永久压在队尾
// （否则新加入的快速域名永远没有机会被发现）。
func (c *Client) domainScoreLocked(d string) time.Duration {
	st, ok := c.domStats[d]
	if !ok || !st.sampled {
		return untestedDomainScore
	}
	score := st.latency
	if score <= 0 {
		score = untestedDomainScore
	}
	if st.failures > 0 {
		score += time.Duration(st.failures) * failurePenalty
	}
	return score
}

const (
	// untestedDomainScore：尚未探测过的域名的初始分数。
	untestedDomainScore = 700 * time.Millisecond
	// failurePenalty：每连续失败一次的加权。
	failurePenalty = 2 * time.Second
)

func (c *Client) LastOKAPIBase() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastOK
}

func (c *Client) doRequest(ctx context.Context, method, fullURL string, form url.Values, header http.Header) (int, []byte, http.Header, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if form != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, gerr := gzip.NewReader(resp.Body)
		if gerr != nil {
			return resp.StatusCode, nil, resp.Header, gerr
		}
		defer gz.Close()
		reader = gz
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, err
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	return resp.StatusCode, data, resp.Header, nil
}

func BuildCookieHeader(cookies map[string]string) string {
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for k, v := range cookies {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

func CaptureSetCookies(h http.Header) map[string]string {
	out := map[string]string{}
	for _, sc := range h.Values("Set-Cookie") {
		idx := strings.IndexByte(sc, '=')
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(sc[:idx])
		rest := sc[idx+1:]
		end := strings.IndexByte(rest, ';')
		val := rest
		if end >= 0 {
			val = rest[:end]
		}
		val = strings.TrimSpace(val)
		if name != "" && val != "" {
			out[name] = val
		}
	}
	return out
}

func looksLikeJSON(body []byte) bool {
	for _, b := range body {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return true
		default:
			return false
		}
	}
	return false
}

func baseHeaders(cookies map[string]string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", UserAgent)
	h.Set("Accept", "application/json")
	h.Set("Accept-Encoding", "gzip")
	if ck := BuildCookieHeader(cookies); ck != "" {
		h.Set("Cookie", ck)
	}
	return h
}

// DecodeDomainServerText decodes the TOS domain-server payload:
// strip leading non-ascii chars -> base64 -> AES-ECB(md5hex(""+DomainServerSecret)) -> {"Server":[...]}.
func DecodeDomainServerText(text string) ([]string, error) {
	cleaned := strings.TrimLeftFunc(strings.TrimSpace(text), func(r rune) bool { return r > 127 })
	raw, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("jm: domain server b64: %w", err)
	}
	key := []byte(MD5Hex("" + DomainServerSecret))
	plain, err := DecryptAESCBPRaw(raw, key)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Server []string `json:"Server"`
	}
	if err := json.Unmarshal(plain, &parsed); err != nil {
		return nil, err
	}
	domains := make([]string, 0, len(parsed.Server))
	for _, d := range parsed.Server {
		d = strings.TrimSpace(d)
		if d != "" {
			domains = append(domains, strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://"))
		}
	}
	return domains, nil
}

func (c *Client) refreshDomainsFromTOS(ctx context.Context) ([]string, bool) {
	for _, u := range DomainServerList {
		tctx, tcancel := context.WithTimeout(ctx, domainTimeout())
		h := http.Header{}
		h.Set("User-Agent", UserAgent)
		status, body, _, err := c.doRequest(tctx, http.MethodGet, u, nil, h)
		tcancel()
		if err != nil || status != http.StatusOK || len(body) == 0 {
			continue
		}
		ds, derr := DecodeDomainServerText(string(body))
		if derr != nil || len(ds) == 0 {
			continue
		}
		c.mu.Lock()
		c.apiDomains = ds
		c.mu.Unlock()
		return ds, true
	}
	return nil, false
}

// APICall 调用上游 API。
//
// 行为：GET 只读接口先查共享响应缓存，未命中再用 single-flight 合并瞬时并发；
// POST（登录、收藏、评论…）与身份强相关接口一律直连上游，不缓存。
func (c *Client) APICall(ctx context.Context, method, path string, query url.Values, form url.Values, cookies map[string]string) (*CallResult, error) {
	cache := c.cache.Load()
	ttl := time.Duration(0)
	if cache != nil {
		ttl = apiCacheTTL(method, path)
	}
	if ttl <= 0 {
		return c.apiCallUpstream(ctx, method, path, query, form, cookies)
	}

	key := apiCacheKey(method, path, query, cookies)
	if res, err, ok := cache.get(key); ok {
		return res, err
	}
	flight, leader := cache.begin(key)
	if !leader {
		// 已经有人在打同一个上游，直接等它的结果，不再重复请求。
		return flight.wait(ctx)
	}
	// leader 用「脱离调用方取消」的上下文去打上游：首个请求可能来自某个刚好断线
	// 的用户，不能让它把同一批等待者的结果一起带走；同时缓存也能被正常填上。
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), apiCacheFillTimeout)
	defer cancel()

	res, err := c.apiCallUpstream(fctx, method, path, query, form, cookies)
	if err != nil {
		if isAPIError(err) {
			// 上游明确回了业务错误（如未登录）：域名是健康的，不要把这种状态
			// 固化进缓存，原样返回给等待者即可。
			cache.finish(key, flight, res, err, 0)
			return res, err
		}
		// 网络/超时类失败：留一个很短的负缓存，避免上游抖动期间被反复打穿。
		cache.finish(key, flight, nil, err, apiCacheNegTTL)
		return nil, err
	}
	cache.finish(key, flight, res, nil, ttl)
	return res, nil
}

// apiCallUpstream 是真正的上游调用，含域名故障转移。
func (c *Client) apiCallUpstream(ctx context.Context, method, path string, query url.Values, form url.Values, cookies map[string]string) (*CallResult, error) {
	upstreamCalls.Add(1)
	var lastErr error
	domains := c.orderedAPIDomains()
	for round := 0; round < maxFailoverRounds; round++ {
		if len(domains) == 0 {
			break
		}
		res, err := c.tryAPIOnDomain(ctx, domains[0], method, path, query, form, cookies)
		if err == nil {
			return res, nil
		}
		if isAPIError(err) {
			return res, err
		}
		lastErr = err

		// 首选域名失败后，让接下来几个候选并行抢跑，谁先成功用谁：把最坏情况的
		// 串行等待（单域名超时 × 域名数）压下来，正常路径不产生任何额外上游流量。
		rest := domains[1:]
		if len(rest) > failoverRaceWidth {
			rest = rest[:failoverRaceWidth]
		}
		if len(rest) > 0 {
			res, err = c.raceAPI(ctx, rest, method, path, query, form, cookies)
			if err == nil {
				return res, nil
			}
			if isAPIError(err) {
				return res, err
			}
			lastErr = err
		}
		if round == 0 {
			if refreshed, ok := c.refreshDomainsFromTOS(ctx); ok {
				domains = c.orderDomains(refreshed)
			} else {
				break
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("jm: all api domains failed")
	}
	c.resetAPIDomains()
	return nil, lastErr
}

// tryAPIOnDomain 在单个域名上做一次完整尝试，并把结果计入该域名的健康度统计。
func (c *Client) tryAPIOnDomain(ctx context.Context, d, method, path string, query url.Values, form url.Values, cookies map[string]string) (*CallResult, error) {
	start := time.Now()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	token, tokenparam := TokenAndTokenParam(ts, TokenSecret)
	h := baseHeaders(cookies)
	h.Set("token", token)
	h.Set("tokenparam", tokenparam)
	full := "https://" + d + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	dctx, dcancel := context.WithTimeout(ctx, domainTimeout())
	status, body, hdr, err := c.doRequest(dctx, method, full, form, h)
	dcancel()
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("jm: HTTP %d from %s", status, d)
	}
	if err != nil {
		c.noteFail(d)
		return nil, err
	}
	if !looksLikeJSON(body) {
		c.noteFail(d)
		return nil, fmt.Errorf("jm: non-json response from %s (status %d)", d, status)
	}
	var env Envelope
	if jerr := json.Unmarshal(body, &env); jerr != nil {
		c.noteFail(d)
		return nil, jerr
	}
	if env.Code != 200 {
		// 域名本身是健康的，只是业务层报错（例如未登录）。
		c.noteOK(d, time.Since(start))
		return &CallResult{Header: hdr}, &APIError{Code: env.Code, Msg: env.ErrorMsg}
	}
	payload := env.Data
	if len(payload) > 0 && payload[0] == '"' {
		var encoded string
		if jerr := json.Unmarshal(payload, &encoded); jerr == nil && encoded != "" {
			dec, derr := DecryptRespData(encoded, ts, "")
			if derr != nil {
				c.noteFail(d)
				return &CallResult{Header: hdr}, derr
			}
			payload = dec
		}
	}
	c.noteOK(d, time.Since(start))
	return &CallResult{Data: payload, Raw: body, Header: hdr}, nil
}

// raceAPI 让多个候选域名同时抢跑，返回第一个可用结果并取消其余尝试。
func (c *Client) raceAPI(ctx context.Context, ds []string, method, path string, query url.Values, form url.Values, cookies map[string]string) (*CallResult, error) {
	if len(ds) == 1 {
		return c.tryAPIOnDomain(ctx, ds[0], method, path, query, form, cookies)
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		res *CallResult
		err error
	}
	ch := make(chan outcome, len(ds))
	for _, d := range ds {
		d := d
		go func() {
			res, err := c.tryAPIOnDomain(rctx, d, method, path, query, form, cookies)
			ch <- outcome{res: res, err: err}
		}()
	}
	var lastErr error
	for range ds {
		o := <-ch
		if o.err == nil || isAPIError(o.err) {
			cancel() // 已有可用结果，取消其余尝试，不浪费上游带宽
			return o.res, o.err
		}
		lastErr = o.err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("jm: all failover candidates failed")
	}
	return nil, lastErr
}

// isAPIError 判断是否为上游业务层错误（区别于网络/超时错误）。
func isAPIError(err error) bool {
	var ae *APIError
	return errors.As(err, &ae)
}

func (c *Client) APIGet(ctx context.Context, path string, query url.Values, cookies map[string]string) (*CallResult, error) {
	return c.APICall(ctx, http.MethodGet, path, query, nil, cookies)
}

func (c *Client) APIPost(ctx context.Context, path string, form url.Values, cookies map[string]string) (*CallResult, error) {
	return c.APICall(ctx, http.MethodPost, path, nil, form, cookies)
}

// PostWebForm posts a form to the legacy web domains in order (used by signup).
func (c *Client) PostWebForm(ctx context.Context, path string, form url.Values, cookies map[string]string) ([]byte, error) {
	var lastErr error
	for _, base := range c.WebDomains() {
		h := http.Header{}
		h.Set("User-Agent", UserAgent)
		h.Set("Referer", base+"/")
		if ck := BuildCookieHeader(cookies); ck != "" {
			h.Set("Cookie", ck)
		}
		status, body, _, err := c.doRequest(ctx, http.MethodPost, base+path, form, h)
		if err != nil {
			lastErr = err
			continue
		}
		if status >= 400 {
			lastErr = fmt.Errorf("jm: web %s http %d", base, status)
			continue
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("jm: all web domains failed")
	}
	return nil, lastErr
}

var scrambleRe = regexp.MustCompile(`var\s+scramble_id\s*=\s*(\d+)\s*;`)

// ScrambleID fetches /chapter_view_template for a chapter's scramble id.
// Falls back to DefaultScrambleID when unavailable.
func (c *Client) ScrambleID(ctx context.Context, chapterID string, cookies map[string]string) (int64, error) {
	q := url.Values{}
	q.Set("id", chapterID)
	q.Set("mode", "vertical")
	q.Set("page", "0")
	q.Set("app_img_shunt", "NaN")
	q.Set("express", "off")
	for _, d := range c.APIDomains() {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		token, tokenparam := TokenAndTokenParam(ts, ContentTokenSecret)
		h := baseHeaders(cookies)
		h.Set("token", token)
		h.Set("tokenparam", tokenparam)
		full := "https://" + d + "/chapter_view_template?" + q.Encode() + "&v=" + ts
		status, body, hdr, err := c.doRequest(ctx, http.MethodGet, full, nil, h)
		_ = hdr
		if err != nil || status != http.StatusOK {
			continue
		}
		if m := scrambleRe.FindSubmatch(body); m != nil {
			id, cerr := strconv.ParseInt(string(m[1]), 10, 64)
			if cerr == nil {
				return id, nil
			}
		}
	}
	return DefaultScrambleID, nil
}

// Latest fetches /latest?page= (GetLatestInfoReq2).
func (c *Client) Latest(ctx context.Context, page string, cookies map[string]string) (*CallResult, error) {
	q := url.Values{}
	q.Set("page", page)
	return c.APIGet(ctx, "/latest", q, cookies)
}

// ChapterViewTemplate fetches the raw /chapter_view_template HTML
// (GetBookEpsScrambleReq2), used by the chapter-detail fallback path.
func (c *Client) ChapterViewTemplate(ctx context.Context, chapterID string, cookies map[string]string) ([]byte, error) {
	q := url.Values{}
	q.Set("id", chapterID)
	q.Set("mode", "vertical")
	q.Set("page", "0")
	q.Set("app_img_shunt", "NaN")
	q.Set("express", "off")
	var lastErr error
	for _, d := range c.APIDomains() {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		token, tokenparam := TokenAndTokenParam(ts, ContentTokenSecret)
		h := baseHeaders(cookies)
		h.Set("token", token)
		h.Set("tokenparam", tokenparam)
		full := "https://" + d + "/chapter_view_template?" + q.Encode() + "&v=" + ts
		status, body, _, err := c.doRequest(ctx, http.MethodGet, full, nil, h)
		if err != nil || status != http.StatusOK {
			lastErr = fmt.Errorf("jm: chapter_view_template from %s failed", d)
			continue
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("jm: all api domains failed")
	}
	return nil, lastErr
}

// ImageURL builds a photo image URL. v=ts is required or the CDN returns empty.
func ImageURL(domain, photoID, filename string, ts int64) string {
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	return fmt.Sprintf("https://%s/media/photos/%s/%s?v=%d", domain, photoID, filename, ts)
}
