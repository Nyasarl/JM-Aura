package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jmaura/internal/jm"
	"jmaura/internal/provider"
	"jmaura/internal/store"
)

const (
	StatusOK              = 1001
	StatusLoad            = 1002
	StatusError           = 1003
	StatusWaitLoad        = 1004
	StatusNetError        = 1005
	StatusUserError       = 1006
	StatusRegisterError   = 1007
	StatusUnKnowError     = 1008
	StatusNotFoundBook    = 1009
	StatusParseError      = 1010
	StatusNeedGoogle      = 1011
	StatusSetHeadError    = 1012
	StatusUnderReviewBook = 1013
	StatusNotLogin        = 1014
	StatusSaveError       = 1015
	StatusAddError        = 1017
	StatusPathError       = 1018
	StatusFileError       = 1019
	StatusFileFormatError = 1020
	StatusTimeOut         = 1021
	StatusConnectErr      = 1022
	StatusSSLErr          = 1023
	StatusResetErr        = 1024
	StatusProxyError      = 1025
	StatusDownloadFail    = 1026
	StatusOfflineModel    = 1027
	StatusDownloadBusy    = 1032
)

func ok(data any, msg string) map[string]any {
	return map[string]any{"st": StatusOK, "msg": msg, "data": data}
}

func errSt(st int, msg string) map[string]any {
	return map[string]any{"st": st, "msg": msg, "data": nil}
}

func errData(st int, msg string, data any) map[string]any {
	return map[string]any{"st": st, "msg": msg, "data": data}
}

func mergeOK(payload map[string]any, msg string) map[string]any {
	out := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		out[k] = v
	}
	if _, has := out["st"]; !has {
		out["st"] = StatusOK
	}
	if _, has := out["msg"]; !has {
		out["msg"] = msg
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func httpDetail(w http.ResponseWriter, code int, detail any) {
	writeJSON(w, code, map[string]any{"detail": detail})
}

func decodeRawMap(b []byte) map[string]any {
	m := map[string]any{}
	if len(b) == 0 {
		return m
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return map[string]any{}
	}
	return m
}

type ctxKey string

const (
	ctxSiteUser ctxKey = "siteUser"
	ctxLoggedIn ctxKey = "loggedIn"
)

func siteUserOf(r *http.Request) (string, bool) {
	u, _ := r.Context().Value(ctxSiteUser).(string)
	logged, _ := r.Context().Value(ctxLoggedIn).(bool)
	return u, logged
}

// effIdentityOf 返回「当前请求 + jm 源」的隔离键，保持既有语义不变。
func effIdentityOf(r *http.Request) string {
	return effIdentityFor(r, string(provider.SourceJM))
}

// effIdentityFor 返回「当前请求 + 指定源」的隔离键：<siteUser>#<source>#<activeUser>。
// 未登录（游客）回落为 g:<gid> / anon，与源无关；因此游客在各源下共享同一个身份串。
func effIdentityFor(r *http.Request, source string) string {
	u, loggedIn := siteUserOf(r)
	identity := strings.TrimSpace(u)
	if identity == "" {
		identity = "anon"
	}
	if loggedIn {
		if active := activeUserForSource(identity, source); active != "" {
			identity = identity + "#" + source + "#" + active
		}
	}
	return identity
}

// activeUserForSource 返回某站点账号在某源下的当前激活上游账号名。
// 目前仅 jm 源有实现；后续接入新源时在此补齐（例如 bika 的用户 token 归属）。
func activeUserForSource(siteUser, source string) string {
	switch source {
	case string(provider.SourceJM):
		return store.CredActiveUsername(siteUser)
	case string(provider.SourceBika):
		// 仅当该站点账号确实持有 bika token 时才算「已登录 bika」。
		return store.BikaCredActive(siteUser)
	}
	return ""
}

func plainSiteUserOf(r *http.Request) string {
	u, loggedIn := siteUserOf(r)
	if !loggedIn {
		return ""
	}
	return strings.TrimSpace(u)
}

func middlewareAllowPath(p string) bool {
	return !strings.HasPrefix(p, "/api/")
}

func shouldSecureCookie(r *http.Request) bool {
	if xf := strings.TrimSpace(strings.ToLower(r.Header.Get("X-Forwarded-Proto"))); xf != "" {
		return xf == "https"
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.URL.Scheme, "https")
}

func Wrap(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				writeJSON(w, 500, map[string]any{"st": 500, "msg": "Internal Server Error", "detail": fmt.Sprint(rec)})
			}
		}()
		bw := &bufWriter{rw: w, accepts: acceptsGzip(r)}
		defer bw.finish()
		if middlewareAllowPath(r.URL.Path) {
			h.ServeHTTP(bw, r)
			return
		} else {
			u, isAuth, newGid := store.EffectiveUser(r)
			onlineTouch(u)
			ctx := context.WithValue(r.Context(), ctxSiteUser, u)
			ctx = context.WithValue(ctx, ctxLoggedIn, isAuth)
			r = r.WithContext(ctx)
			if newGid != "" {
				http.SetCookie(bw, &http.Cookie{
					Name:     "jm_aura_gid",
					Value:    newGid,
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					Secure:   shouldSecureCookie(r),
					MaxAge:   365 * 86400,
				})
			}
			h.ServeHTTP(bw, r)
		}
	})
}

const gzipMinSize = 800

func acceptsGzip(r *http.Request) bool {
	return acceptsEncoding(r, "gzip")
}

// acceptsEncoding 判断客户端是否接受指定的 Content-Encoding。
// 正确解析 q 值：「gzip;q=0」表示明确不接受，不能当成接受。
func acceptsEncoding(r *http.Request, enc string) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), enc) {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			p = strings.TrimSpace(p)
			if v, ok := strings.CutPrefix(p, "q="); ok {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		return q > 0
	}
	return false
}

// addVary 追加 Vary 头，已存在同名字段时不重复添加。
func addVary(hdr http.Header, value string) {
	for _, existing := range hdr.Values("Vary") {
		for _, token := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return
			}
		}
	}
	hdr.Add("Vary", value)
}

type bufWriter struct {
	rw        http.ResponseWriter
	accepts   bool
	status    int
	statusSet bool
	buf       bytes.Buffer
	decided   bool
	useGzip   bool
	gzw       *gzip.Writer
	sink      io.Writer
}

func (b *bufWriter) Header() http.Header { return b.rw.Header() }

func (b *bufWriter) canGzip() bool {
	if !b.accepts {
		return false
	}
	hdr := b.rw.Header()
	if hdr.Get("Content-Encoding") != "" {
		return false
	}
	if ct := hdr.Get("Content-Type"); strings.HasPrefix(ct, "image/") {
		return false
	}
	st := b.status
	if !b.statusSet {
		st = 200
	}
	return st >= 200 && st != 204 && st != 304
}

func (b *bufWriter) decide(startGzip bool) {
	if b.decided {
		return
	}
	b.decided = true
	if startGzip && b.canGzip() {
		hdr := b.rw.Header()
		hdr.Del("Content-Length")
		hdr.Set("Content-Encoding", "gzip")
		addVary(hdr, "Accept-Encoding")
		b.gzw = gzip.NewWriter(b.rw)
		b.sink = b.gzw
		b.useGzip = true
	} else {
		b.sink = b.rw
	}
	// 无论是否压缩都要声明 Vary：同一 URL 对不同客户端可能返回不同编码，
	// 缺少 Vary 会让共享缓存把 gzip 版本发给不支持压缩的客户端。
	addVary(b.rw.Header(), "Accept-Encoding")
	st := b.status
	if !b.statusSet {
		st = 200
	}
	b.rw.WriteHeader(st)
	if b.buf.Len() > 0 {
		_, _ = b.sink.Write(b.buf.Bytes())
		b.buf.Reset()
	}
}

func (b *bufWriter) WriteHeader(code int) {
	if b.statusSet || b.decided {
		return
	}
	b.status = code
	b.statusSet = true
	if b.buf.Len() > gzipMinSize {
		b.decide(true)
	}
}

func (b *bufWriter) Write(p []byte) (int, error) {
	if b.decided {
		return b.sink.Write(p)
	}
	b.buf.Write(p)
	if b.buf.Len() > gzipMinSize {
		b.decide(true)
	}
	return len(p), nil
}

func (b *bufWriter) Flush() {
	if !b.decided {
		b.decide(false)
	}
	if f, ok := b.sink.(http.Flusher); ok {
		f.Flush()
	}
}

func (b *bufWriter) finish() {
	if b.decided {
		if b.useGzip && b.gzw != nil {
			_ = b.gzw.Close()
		}
		return
	}
	b.decide(false)
}

type rateLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	order     []string // 键的插入顺序，用于 O(1) 淘汰最老的键
	head      int
	window    time.Duration
	lastSweep time.Time
}

func newRateLimiter(window time.Duration) *rateLimiter {
	return &rateLimiter{hits: map[string][]time.Time{}, window: window}
}

func (l *rateLimiter) allow(key string, max int) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	arr, known := l.hits[key]
	keep := arr[:0]
	for _, t := range arr {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) >= max {
		l.hits[key] = keep
		return false
	}
	if !known {
		// 新键：先把表压回上限以内，再放进来
		l.evictIfFull()
		l.order = append(l.order, key)
	}
	l.hits[key] = append(keep, now)
	// 清理已经彻底过期的键：hits 只在对应客户端再次命中时才会被回收，
	// 而键本身来自来访者 IP，长期运行会让这张 map 无限增长。
	// 清扫最多一个窗口做一次，避免每个请求都扫全表。
	if len(l.hits) > rateLimiterMaxKeys && now.Sub(l.lastSweep) >= l.window {
		l.lastSweep = now
		l.sweepLocked(now)
	}
	return true
}

// evictIfFull 表满时淘汰最老的键，保证限流表的大小始终有上界。
// 调用方必须持有锁。
func (l *rateLimiter) evictIfFull() {
	for len(l.hits) >= rateLimiterMaxKeys && l.head < len(l.order) {
		k := l.order[l.head]
		l.head++
		delete(l.hits, k)
	}
	// order 自己也不能无限增长，定期把已经消费掉的前缀丢掉
	if l.head > 1024 && l.head*2 > len(l.order) {
		l.order = append(l.order[:0], l.order[l.head:]...)
		l.head = 0
	}
}

// sweepLocked 清掉过期键；如果清完仍然超限，再按「最久没访问」淘汰，
// 保证这张表的大小始终有上界。调用方必须持有锁。
func (l *rateLimiter) sweepLocked(now time.Time) {
	for k, ts := range l.hits {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= l.window {
			delete(l.hits, k)
		}
	}
	if len(l.hits) <= rateLimiterMaxKeys {
		return
	}
	oldest := make([]string, 0, len(l.hits))
	oldestAt := make(map[string]time.Time, len(l.hits))
	for k, ts := range l.hits {
		last := time.Time{}
		if len(ts) > 0 {
			last = ts[len(ts)-1]
		}
		oldest = append(oldest, k)
		oldestAt[k] = last
	}
	sort.Slice(oldest, func(i, j int) bool {
		return oldestAt[oldest[i]].Before(oldestAt[oldest[j]])
	})
	for i := 0; i < len(oldest) && len(l.hits) > rateLimiterMaxKeys; i++ {
		delete(l.hits, oldest[i])
	}
}

func remoteAddrKey(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-For")
	if host != "" {
		parts := strings.Split(host, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type promoteEntry struct {
	exp  time.Time
	body []byte
}

const (
	promoteCacheTTL    = 5 * time.Second
	promoteCacheMax    = 16      // 缓存条目上限，键空间归一化后本就很小
	promoteCacheMaxLen = 4 << 20 // 单条正文上限，超大响应不入缓存
	maxPageParam       = 100     // 分页参数上限，避免出现无意义的深层翻页
	rateLimiterMaxKeys = 4096    // 限流表键数上限，超过后清理过期键
)

// normalizePageParam 归一化分页参数。上游只认数字页码，非法值一律回落到 0。
// 这样缓存键就不会被用户可控的原始字符串撑成无限多个。
func normalizePageParam(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "0"
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return "0"
	}
	if n > maxPageParam {
		return strconv.Itoa(maxPageParam)
	}
	return strconv.Itoa(n)
}

var (
	promoteMu    sync.Mutex
	promoteCache = map[string]promoteEntry{}
)

func promoteGet(page string) ([]byte, bool) {
	promoteMu.Lock()
	defer promoteMu.Unlock()
	e, has := promoteCache[page]
	if !has || time.Now().After(e.exp) {
		if has {
			delete(promoteCache, page)
		}
		return nil, false
	}
	out := make([]byte, len(e.body))
	copy(out, e.body)
	return out, true
}

func promotePut(page string, body []byte) {
	if len(body) > promoteCacheMaxLen {
		return
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	promoteMu.Lock()
	defer promoteMu.Unlock()
	now := time.Now()
	// 顺手清掉过期条目：否则没人再访问的键会一直留在表里
	for k, v := range promoteCache {
		if now.After(v.exp) {
			delete(promoteCache, k)
		}
	}
	if _, exists := promoteCache[page]; !exists && len(promoteCache) >= promoteCacheMax {
		// 满了就淘汰最接近过期的那条，保证新请求仍然能进缓存
		oldestKey := ""
		var oldestExp time.Time
		for k, v := range promoteCache {
			if oldestKey == "" || v.exp.Before(oldestExp) {
				oldestKey, oldestExp = k, v.exp
			}
		}
		delete(promoteCache, oldestKey)
	}
	promoteCache[page] = promoteEntry{exp: now.Add(promoteCacheTTL), body: cp}
}

func is401Err(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 401")
}

func liveJMSession(r *http.Request) bool {
	ck := store.LoadCookies(effIdentityOf(r))
	if len(ck) == 0 {
		return false
	}
	cli := jm.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if _, ferr := cli.Favorite(ctx, 1, "0", "", ck); ferr != nil {
		if is401Err(ferr) {
			return false
		}
		return true
	}
	return true
}

func opYmlPath() string {
	if v := strings.TrimSpace(os.Getenv("JM_AURA_CONFIG_PATH")); v != "" {
		return v
	}
	cands := []string{
		filepath.Join("config", "op.yml"),
		filepath.Join("..", "config", "op.yml"),
		filepath.Join(store.DataDir(), "op.yml"),
	}
	for _, c := range cands {
		if st, serr := os.Stat(c); serr == nil && !st.IsDir() {
			return c
		}
	}
	return cands[0]
}

func opYmlClientBlock(path string) (lines []string, ok bool) {
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		return nil, false
	}
	return strings.Split(string(b), "\n"), true
}

func opLineIndent(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

func opKV(body string) (string, string, bool) {
	i := strings.Index(body, ":")
	if i < 0 {
		return "", "", false
	}
	k := strings.TrimSpace(body[:i])
	v := strings.TrimSpace(body[i+1:])
	return k, v, k != ""
}

func yamlUnquote(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

func parseOpYmlClient(path string) (u, pw string, hasKeys bool) {
	lines, okRead := opYmlClientBlock(path)
	if !okRead {
		return "", "", false
	}
	inClient := false
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := opLineIndent(line)
		body := strings.TrimSpace(line)
		if indent == 0 {
			inClient = body == "client:" || strings.HasPrefix(body, "client:")
			continue
		}
		if !inClient {
			continue
		}
		if k, v, isKV := opKV(body); isKV {
			switch k {
			case "username":
				u = yamlUnquote(v)
				hasKeys = true
			case "password":
				pw = yamlUnquote(v)
				hasKeys = true
			}
		}
	}
	return u, pw, hasKeys
}

func migrateOpYmlCredentials(targetSiteUser string) {
	path := opYmlPath()
	u, pw, hasKeys := parseOpYmlClient(path)
	if u != "" && pw != "" && !store.CredHas(targetSiteUser) {
		_ = store.CredSet(targetSiteUser, u, pw)
	}
	if hasKeys {
		clearOpYmlCredsAt(path)
	}
}

func clearOpYmlCredsAt(path string) {
	lines, okRead := opYmlClientBlock(path)
	if !okRead {
		return
	}
	out := make([]string, 0, len(lines))
	inClient := false
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		indent := opLineIndent(line)
		body := strings.TrimSpace(line)
		if body == "" {
			out = append(out, line)
			continue
		}
		if indent == 0 {
			inClient = strings.HasPrefix(body, "client:")
			out = append(out, line)
			continue
		}
		if inClient {
			if k, _, isKV := opKV(body); isKV && (k == "username" || k == "password") {
				continue
			}
		}
		out = append(out, line)
	}
	_ = os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}
