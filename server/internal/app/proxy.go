package app

import (
	"bytes"
	"container/list"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const chrome120UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

var (
	proxyClientVerify   = newProxyClient(true)
	proxyClientNoVerify = newProxyClient(false)
)

func newProxyClient(verify bool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			// 图床是 HTTPS 且支持 HTTP/2；Go 只要看到 DialContext/TLSClientConfig
			// 被显式设置就保守地关掉 h2，必须显式兜回来。否则 200 并发下会退化
			// 成一堆新连接 + 握手（多花 1~2 个 RTT，还吃本机 CPU/TIME_WAIT）。
			ForceAttemptHTTP2: true,
			// 池子要能撑住"同一份热门章节被几十上百人同时打开"的并发，否则
			// MaxIdleConnsPerHost 太小会退化成反复握手。
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   64,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: !verify},
		},
	}
}

func streamImageResponse(w http.ResponseWriter, resp *http.Response, cacheControl string) {
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", cacheControl)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, resp.Body)
}

// drainClose 读完并关闭响应体（带上限），让底层连接可被复用。
func drainClose(resp *http.Response) {
	if resp == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

// —— 按需缩略 ——
//
// 列表封面按 w 参数缩到指定宽度（只缩小、不放大）后再回发，避免浏览器解码整张大图
// （实测首页封面按原图解码约 130MB，实际显示只需 ~5MB）。
// 缩放结果进有字节上限的 LRU 缓存，同一张图重复请求不再解码。详情/阅读页仍用原图。

const (
	imgThumbMaxBytes = 24 << 20 // 缩略图缓存上限（字节）
	imgThumbMinW     = 32       // w 下限
	imgThumbMaxW     = 1600     // w 上限
	imgThumbMaxBody  = 12 << 20 // 单张原图读取上限
)

type imgThumbEntry struct {
	key  string
	data []byte
	ct   string
}

type imgThumbCache struct {
	mu    sync.Mutex
	order *list.List
	items map[string]*list.Element
	bytes int
	max   int
}

// stats 返回当前缓存条目数与占用字节数（供 /api/v2/cache/stats 观察）。
func (c *imgThumbCache) stats() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items), c.bytes
}

var imgThumbs = &imgThumbCache{order: list.New(), items: map[string]*list.Element{}, max: imgThumbMaxBytes}

func (c *imgThumbCache) get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		e := el.Value.(*imgThumbEntry)
		return e.data, e.ct, true
	}
	return nil, "", false
}

func (c *imgThumbCache) put(key string, data []byte, ct string) {
	if len(data) == 0 || len(data) > c.max {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		e := el.Value.(*imgThumbEntry)
		c.bytes += len(data) - len(e.data)
		e.data, e.ct = data, ct
	} else {
		c.items[key] = c.order.PushFront(&imgThumbEntry{key: key, data: data, ct: ct})
		c.bytes += len(data)
	}
	for c.bytes > c.max {
		back := c.order.Back()
		if back == nil {
			break
		}
		e := back.Value.(*imgThumbEntry)
		c.order.Remove(back)
		delete(c.items, e.key)
		c.bytes -= len(e.data)
	}
}

// webpDisplayRGB 修正有损 WebP 的 YCbCr→RGB 换算。
//
// golang.org/x/image/webp 对 VP8(有损) 返回 *image.YCbCr，其像素若走 Go 的全范围
// YCbCrToRGB 会整体偏暗（纯白 255 → 约 240，白底泛灰）；浏览器/libwebp 使用「有限范围
// (studio) BT.601」换算。这里按同一口径转换，保证服务端渲染与浏览器观感一致。
// 无损 WebP（已是 RGBA/NRGBA）与其它格式原样返回。
func webpDisplayRGB(img image.Image) image.Image {
	yc, ok := img.(*image.YCbCr)
	if !ok {
		return img
	}
	b := yc.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	clamp := func(v int) uint8 {
		switch {
		case v < 0:
			return 0
		case v > 255:
			return 255
		default:
			return uint8(v)
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yy := int(yc.Y[yc.YOffset(x+b.Min.X, y+b.Min.Y)]) - 16
			if yy < 0 {
				yy = 0
			}
			cb := int(yc.Cb[yc.COffset(x+b.Min.X, y+b.Min.Y)]) - 128
			cr := int(yc.Cr[yc.COffset(x+b.Min.X, y+b.Min.Y)]) - 128
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = clamp((298*yy + 409*cr + 128) >> 8)
			dst.Pix[i+1] = clamp((298*yy - 100*cb - 208*cr + 128) >> 8)
			dst.Pix[i+2] = clamp((298*yy + 516*cb + 128) >> 8)
			dst.Pix[i+3] = 255
		}
	}
	return dst
}

// imgDownscale 把图片缩到 maxW 宽（仅缩小）。无需缩放或解码失败时 ok=false。
func imgDownscale(raw []byte, maxW int) (data []byte, ct string, ok bool) {
	src, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", false
	}
	// 仅当源图确实来自有损 WebP 时才做色彩修正（见 webpDisplayRGB）。
	// JPEG 走 Go 原生换算即是浏览器观感，套用 WebP 的有限范围公式会偏色，
	// 且白付一次全图逐像素转换 + 一次整图内存分配。
	if format == "webp" {
		src = webpDisplayRGB(src)
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || sw <= maxW {
		return nil, "", false
	}
	dw := maxW
	dh := sh * dw / sw
	if dh < 1 {
		dh = 1
	}
	// 目标色模型决定 x/image/draw 走哪条路径：只有 *image.RGBA 才命中针对
	// YCbCr(JPEG)/NRGBA/RGBA 源图的定长快路径（直接读 Pix、零分配）；用 *image.NRGBA
	// 会落到逐接口调用的通用实现，每张封面约 80 万次分配。封面绝大多数是不透明的
	// JPEG/WebP，因此不透明时用 RGBA；仅带透明的图保留 NRGBA 以输出正确的直通 alpha。
	op, hasAlpha := src.(interface{ Opaque() bool })
	opaque := !hasAlpha || op.Opaque()
	rect := image.Rect(0, 0, dw, dh)
	var dst draw.Image
	if opaque {
		dst = image.NewRGBA(rect)
	} else {
		dst = image.NewNRGBA(rect)
	}
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	var buf bytes.Buffer
	if !opaque {
		ct = "image/png"
		if err := png.Encode(&buf, dst); err != nil {
			return nil, "", false
		}
	} else {
		ct = "image/jpeg"
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
			return nil, "", false
		}
	}
	return buf.Bytes(), ct, true
}

func writeImage(w http.ResponseWriter, ct string, data []byte) {
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func handleImageProxy(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	if raw == "" {
		httpDetail(w, 400, "Missing URL")
		return
	}
	u, perr := url.Parse(raw)
	if perr != nil {
		httpDetail(w, 500, perr.Error())
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		httpDetail(w, 400, "Invalid scheme")
		return
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "192.168.") {
		httpDetail(w, 403, "Access denied")
		return
	}
	width := 0
	if q := r.URL.Query().Get("w"); q != "" {
		if n, perr := strconv.Atoi(q); perr == nil && n >= imgThumbMinW && n <= imgThumbMaxW {
			width = n
		}
	}
	cacheKey := ""
	if width > 0 {
		cacheKey = strconv.Itoa(width) + "|" + raw
		if data, ct, hit := imgThumbs.get(cacheKey); hit {
			writeImage(w, ct, data)
			return
		}
	}
	ref := "https://jmcomic.me/"
	if u.Scheme != "" && u.Host != "" {
		ref = u.Scheme + "://" + u.Host + "/"
	}
	req, rerr := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
	if rerr != nil {
		httpDetail(w, 500, rerr.Error())
		return
	}
	req.Header.Set("Referer", ref)
	req.Header.Set("User-Agent", chrome120UA)
	resp, ferr := proxyClientVerify.Do(req)
	if ferr != nil {
		// 部分第三方图源（如哔咔的 diwodiwo 系列 CDN）证书链不完整，
		// 严格校验会失败；此处回退到免校验客户端，避免整站图片挂掉。
		req2, rerr2 := http.NewRequestWithContext(r.Context(), http.MethodGet, raw, nil)
		if rerr2 != nil {
			httpDetail(w, 500, ferr.Error())
			return
		}
		req2.Header.Set("Referer", ref)
		req2.Header.Set("User-Agent", chrome120UA)
		resp, ferr = proxyClientNoVerify.Do(req2)
		if ferr != nil {
			httpDetail(w, 500, ferr.Error())
			return
		}
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		httpDetail(w, resp.StatusCode, "Image fetch failed")
		return
	}
	if width <= 0 {
		streamImageResponse(w, resp, "public, max-age=86400")
		return
	}
	body, rerr2 := io.ReadAll(io.LimitReader(resp.Body, imgThumbMaxBody))
	if rerr2 != nil {
		writeImage(w, resp.Header.Get("Content-Type"), body)
		return
	}
	if out, ct, ok := imgDownscale(body, width); ok {
		imgThumbs.put(cacheKey, out, ct)
		writeImage(w, ct, out)
		return
	}
	// 解码失败或本就够小：原样回发已读字节。
	writeImage(w, resp.Header.Get("Content-Type"), body)
}

// —— 在线阅读图片解码 ——
//
// JM 章节图为分块乱序图。旧实现把原图原样转发、由前端 canvas 还原：每张图会生成与原图
// 等大的 canvas 位图并一直保留，十几张累积即逼近 iOS Safari 的 canvas 总内存上限
// （约设备内存 1/4），超限后 getContext 返回 null 或画布被静默清空，表现为「图片没有解码」。
// 现改为在此处还原后回发，前端只需普通 <img>，彻底绕开该上限。
//
// 结果进有字节上限的 LRU，避免热门章节被反复解码；并用信号量限制并发，控制峰值内存。

const (
	imgDecodedMaxBytes = 48 << 20 // 已解码章节图缓存上限（字节）
	imgDecodeMaxBody   = 24 << 20 // 单张原图读取上限（字节）
	imgDecodeQuality   = 90       // 章节图重编码质量（高于下载用的 75，阅读更接近无损）
	imgDecodeMaxConc   = 8        // 同时解码上限
)

var (
	imgDecoded   = &imgThumbCache{order: list.New(), items: map[string]*list.Element{}, max: imgDecodedMaxBytes}
	imgDecodeSem = make(chan struct{}, imgDecodeMaxConc)
)

// writeChapterImage 回发章节图，沿用旧的 1 年浏览器缓存。
func writeChapterImage(w http.ResponseWriter, ct string, data []byte) {
	if ct == "" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=31536000")
	// 显式给出长度：否则 Go 只能分块传输（chunked），浏览器无法显示进度，
	// 中间层也没法按长度调度。
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// decodeChapterImage 在并发上限内反置乱一张章节图；ctx 取消时直接放弃。
func decodeChapterImage(ctx context.Context, body []byte, epsID, scrambleID int, picName string) ([]byte, string, bool) {
	select {
	case imgDecodeSem <- struct{}{}:
		defer func() { <-imgDecodeSem }()
	case <-ctx.Done():
		return nil, "", false
	}
	return descrambleImageBytes(body, dlSegmentationNum(epsID, scrambleID, picName), false, imgDecodeQuality)
}

func handleChapterImage(w http.ResponseWriter, r *http.Request) {
	photoID := r.PathValue("photo_id")
	imageName := r.PathValue("image_name")
	domain := r.URL.Query().Get("domain")
	scrambleID := atoiDefault(r.URL.Query().Get("scramble"), 0)

	epsID, _ := strconv.Atoi(strings.TrimSpace(photoID))
	picName := imageName
	if i := strings.Index(imageName, "."); i > 0 {
		picName = imageName[:i]
	}
	// 仅在需要且可还原时解码：GIF 不置乱，epsID < scrambleID 的章节也不置乱。
	wantDecode := scrambleID > 0 &&
		!strings.HasSuffix(strings.ToLower(imageName), ".gif") &&
		dlSegmentationNum(epsID, scrambleID, picName) > 1

	if wantDecode {
		if data, ct, hit := imgDecoded.get(chapterImageDecodeKey(scrambleID, photoID, imageName)); hit {
			writeChapterImage(w, ct, data)
			return
		}
	}

	candidates := chapterImageCandidates(domain)

	// 整体超时：以前只有「响应头超时」和调用方断线，body 读到一半卡死会一直挂着。
	ctx, cancel := context.WithTimeout(r.Context(), chapterImageTimeout)
	defer cancel()

	// 「同一 URL 并发合并」：热门章节被几十上百人同时打开时，以前每个请求都会各自
	// 打一次图床（再各自解码一次），现在是同一张图只抓一次，其余请求复用同一份字节。
	res := chapterImageFetchMerged(ctx, chapterImageFlightKey(photoID, imageName), func(fctx context.Context) imgFetchResult {
		return fetchChapterImageRaw(fctx, candidates, photoID, imageName)
	})

	if errors.Is(res.err, errImageTooLarge) {
		// 超过缓冲上限的少数大图：退化为直接流式转发，不做合并。
		streamChapterImageDirect(w, ctx, candidates, photoID, imageName)
		return
	}
	if res.err != nil {
		status := res.status
		if status == 0 {
			status = http.StatusBadGateway
		}
		httpDetail(w, status, "Image not found")
		return
	}

	if !wantDecode {
		writeChapterImage(w, res.ct, res.body)
		return
	}
	if out, ct, ok := decodeChapterImage(ctx, res.body, epsID, scrambleID, picName); ok {
		imgDecoded.put(chapterImageDecodeKey(scrambleID, photoID, imageName), out, ct)
		writeChapterImage(w, ct, out)
		return
	}
	// 解码失败（格式不支持/图片损坏）：原样回发，避免整页空白。
	writeChapterImage(w, http.DetectContentType(res.body), res.body)
}

// ── 章节图抓取：候选域名、超时、并发合并 ──────────────────────────────────────

const (
	// imgFetchMaxBody 是「可被合并复用」的单张图片字节上限。真实章节图（webp/jpeg）
	// 通常只有几十 KB 到 1 MB，这里给足余量，同时避免极端输入把内存吃光。
	imgFetchMaxBody = 12 << 20
	// chapterImageTimeout 是一次章节图抓取（含故障转移）的总预算。
	chapterImageTimeout = 25 * time.Second
)

// errImageTooLarge 表示图片超过合并缓冲上限，调用方应退化为直接流式转发。
var errImageTooLarge = errors.New("chapter image too large to buffer")

// chapterImageDecodeKey 是解码结果的缓存键。
func chapterImageDecodeKey(scrambleID int, photoID, imageName string) string {
	return strconv.Itoa(scrambleID) + "|" + photoID + "|" + imageName
}

// chapterImageFlightKey 是并发合并的键：同一张图（同图床路径）只抓一次。
func chapterImageFlightKey(photoID, imageName string) string {
	return photoID + "|" + imageName
}

// chapterImageCandidates 返回按优先级排列的候选图床域名。
func chapterImageCandidates(domain string) []string {
	candidates := []string{}
	dv := domain
	if dv != "" && !strings.Contains(dv, ":") && !strings.Contains(dv, "localhost") &&
		!strings.HasPrefix(dv, "192.168.") && !strings.HasPrefix(dv, "127.") {
		candidates = append(candidates, dv)
	}
	for _, h := range apiClient().ImageDomains() {
		if h != "" {
			candidates = append(candidates, h)
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, "cdn-msp.jmapinodeudzn.net")
	}
	return candidates
}

type imgFetchResult struct {
	body   []byte
	ct     string
	status int
	err    error
}

type imgFlight struct {
	done chan struct{}
	res  imgFetchResult
}

var chapterImageFlights = struct {
	mu sync.Mutex
	m  map[string]*imgFlight
}{m: map[string]*imgFlight{}}

// chapterImageFetchMerged 保证同一 key 的并发请求只执行一次 fetch，其余等待结果。
func chapterImageFetchMerged(ctx context.Context, key string, fetch func(context.Context) imgFetchResult) imgFetchResult {
	chapterImageFlights.mu.Lock()
	if f, ok := chapterImageFlights.m[key]; ok {
		chapterImageFlights.mu.Unlock()
		select {
		case <-f.done:
			return f.res
		case <-ctx.Done():
			return imgFetchResult{err: ctx.Err()}
		}
	}
	f := &imgFlight{done: make(chan struct{})}
	chapterImageFlights.m[key] = f
	chapterImageFlights.mu.Unlock()

	// 首位请求方用自己的 ctx 去抓；但如果它中途断线，不能让同一批等待者也失败，
	// 所以这里脱掉取消信号，只保留整体超时。
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), chapterImageTimeout)
	res := fetch(fctx)
	cancel()

	f.res = res
	chapterImageFlights.mu.Lock()
	delete(chapterImageFlights.m, key)
	chapterImageFlights.mu.Unlock()
	close(f.done)
	return res
}

// fetchChapterImageRaw 抓取一张章节图：任何一个候选域名的网络错误、超时或非 200
// 都会换下一个域名继续试（修复以前「首个域名致命错误就直接 500 裂图」的问题）。
func fetchChapterImageRaw(ctx context.Context, candidates []string, photoID, imageName string) imgFetchResult {
	seen := map[string]bool{}
	lastErr := error(nil)
	lastStatus := 0
	for _, host := range candidates {
		if seen[host] {
			continue
		}
		seen[host] = true

		imgURL := "https://" + host + "/media/photos/" + photoID + "/" + imageName
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Referer", "https://"+host+"/")
		req.Header.Set("User-Agent", chrome120UA)

		resp, err := proxyClientNoVerify.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		lastStatus = resp.StatusCode
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("chapter image: HTTP %d from %s", resp.StatusCode, host)
			continue
		}
		ct := resp.Header.Get("Content-Type")
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, imgFetchMaxBody+1))
		_ = resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			continue
		}
		if int64(len(body)) > imgFetchMaxBody {
			return imgFetchResult{status: http.StatusOK, err: errImageTooLarge}
		}
		if ct == "" {
			ct = http.DetectContentType(body)
		}
		return imgFetchResult{body: body, ct: ct, status: http.StatusOK}
	}
	if lastErr == nil {
		lastErr = errors.New("chapter image: no candidate host available")
	}
	return imgFetchResult{status: lastStatus, err: lastErr}
}

// streamChapterImageDirect 是超出缓冲上限时的退路：直接边读边转发，不经过内存缓冲。
func streamChapterImageDirect(w http.ResponseWriter, ctx context.Context, candidates []string, photoID, imageName string) {
	seen := map[string]bool{}
	lastStatus := 0
	for _, host := range candidates {
		if seen[host] {
			continue
		}
		seen[host] = true
		imgURL := "https://" + host + "/media/photos/" + photoID + "/" + imageName
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Referer", "https://"+host+"/")
		req.Header.Set("User-Agent", chrome120UA)
		resp, err := proxyClientNoVerify.Do(req)
		if err != nil {
			continue
		}
		lastStatus = resp.StatusCode
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			continue
		}
		streamImageResponse(w, resp, "public, max-age=31536000")
		_ = resp.Body.Close()
		return
	}
	if lastStatus == 0 {
		lastStatus = http.StatusNotFound
	}
	httpDetail(w, lastStatus, "Image not found")
}
