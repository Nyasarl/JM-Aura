package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// —— 封面（缩略图）链路优化 ——
//
// 首页一次要拉 180 多张封面，这条链路原本有四个问题：
//
//  1. 变体选错：同一个封面在不同镜像域名上分辨率并不一样——
//     cdn-msp.jmapiproxy1.cc 给的是 306×306 的小变体（13KB），
//     其它镜像给的是 600×600 的大变体（31KB）。前端给 /api/promote 的
//     硬编码域名恰好是大变体，于是服务端每次都要下载 31KB、解码整幅
//     600×600、再缩到 320 重编码；而小变体直接透传即可。
//     （v2 接口一直走的是小变体，只有 /api/promote 走了大变体。）
//  2. 同一张封面被并发请求时没有合并，每个请求各自打一次上游。
//  3. 上游取图没有并发上限，几十张封面同时打过去，2 核机器的 CPU 和
//     上游连接数一起被打满，首张封面要等 1 秒多。
//  4. 上游会随机把连接挂住（实测 24 并发里就有一个卡了 30 秒），
//     而取图既没有超时也没有备用域名：挂住的连接长期占住额度，
//     队列越堆越长，用户看到的就是「封面一直转圈不显示」。
//
// 这里把 2~4 补齐，1 则由「按请求宽度挑变体」解决，
// 再加上「列表接口返回后后台预热」：用户滚动到某一行时封面早已在缓存里，
// 命中即返回。

const (
	// coverVariantMinRatio 源图宽度低于目标宽度的这个比例就算「偏小」，
	// 值得再等等看有没有更合适的变体。请求 320 宽时，306px 的小变体
	// 完全够用（差 4%），不值得为此去拉 600px 的大变体。
	coverVariantMinRatio = 0.85

	// coverVariantMaxRatio 源图宽度不超过目标宽度的这个倍数时，直接原样透传，
	// 不做缩放：既省掉一次整幅解码 + 重编码（纯 Go 解 WebP 要 100~300ms CPU，
	// 这台部署机只有 2 核），又不必把更大的图发给客户端。
	coverVariantMaxRatio = 1.0

	// imgFetchMaxConc 同时在飞的上游取图数。
	// 太小则首页几十张封面要排队，太大则会在 2 核机器上把 CPU 和上游连接一起打满。
	imgFetchMaxConc = 12

	// coverPrewarmConc 后台预热的并发数。预热是尽力而为，绝不能拖慢用户自己的请求。
	coverPrewarmConc = 2

	// coverPrewarmDelay 列表响应之后延迟多久才开始预热。
	// 用户点开首页时会立刻触发几十张封面的上游取图，这段时间上游最忙，
	// 预热必须等这波过去再开始，否则就是在抢用户自己的带宽。
	coverPrewarmDelay = 2 * time.Second

	// coverPrewarmBackoff 预热抢不到上游额度时的退避时间。
	coverPrewarmBackoff = 500 * time.Millisecond

	// coverPrewarmLimit 单次列表响应最多预热多少张封面。
	coverPrewarmLimit = 240

	// coverFetchTimeout 单张封面的上游取图总超时。
	coverFetchTimeout = 20 * time.Second

	// coverHedgeDelay 竞速取图的「发车」间隔：第一个候选超过这个时间还没回响应头，
	// 就并行发下一个镜像域名顶上，谁先回用谁。
	// 这个时间要明显大于上游的正常延迟（高并发下也有 150~200ms），
	// 否则等于把上游流量翻倍，反而更慢。

	// coverPrewarmWait 预热任务等待上游额度的最长时间。
	// 抢不到就直接跳过这一张：预热是尽力而为，绝不能拖慢用户自己的请求。
	coverPrewarmWait = 150 * time.Millisecond

	// coverCacheSeconds 封面缩略图的浏览器缓存时长。
	// URL 里带了上游地址与目标宽度，内容实际不可变，给足缓存能显著减少重复下载。
	coverCacheSeconds = 7 * 24 * 3600
)

var (
	// coverCandidateTimeout 单个候选域名的取图超时（变量：便于测试调小）。
	// 上游会随机把连接挂住（一次 24 并发里就有一个卡了 30 秒），
	// 没有超时的话，一个挂起的连接会长期占住额度槽位，把整条队列拖死——
	// 表现就是「封面一直转圈不显示」。
	coverCandidateTimeout = 3 * time.Second

	// coverHedgeDelay 竞速取图的「发车」间隔：第一个候选超过这个时间还没回响应头，
	// 就并行发下一个镜像域名顶上，谁先回用谁。
	// 这个时间要明显大于上游的正常延迟（高并发下也有 150~400ms），
	// 否则等于把上游流量翻倍，反而更慢。
	coverHedgeDelay = 1200 * time.Millisecond

	// imgFetchSem 限制同时在飞的上游取图数（用户请求与后台预热共用）。
	imgFetchSem = make(chan struct{}, imgFetchMaxConc)

	// coverHostWidth 记录各镜像域名上封面图的典型宽度（像素）。
	// 各镜像给的分辨率并不一致，记下来就不用每次都先抓一张再判断够不够用。
	coverHostWidthMu sync.Mutex
	coverHostWidth   = map[string]int{}

	// jmImageHostRe 匹配 JM 图床域名（只有这些域名之间可以互相回退）。
	jmImageHostRe = regexp.MustCompile(`^cdn-msp[0-9]*\.(?:jmapiproxy[0-9]*|jmapinode[0-9]*|jmapinodeudzn)\.(?:cc|net)$`)

	// coverFlights 封面取取的 single-flight 表：同一张封面被并发请求时只抓一次。
	coverFlights   = map[string]*coverFlight{}
	coverFlightsMu sync.Mutex

	// coverProbed 记录已经探测过分辨率的镜像域名。
	coverProbeMu sync.Mutex
	coverProbed  = map[string]bool{}
)

var (
	// errFetchStatus 表示上游取图失败。
	errFetchStatus = errors.New("upstream image fetch failed")
	// errFetchBusy 表示预热任务没抢到上游额度。
	errFetchBusy = errors.New("image fetch slot busy")
)

// coverFlight 是一次共享中的上游取图。
type coverFlight struct {
	done   chan struct{}
	body   []byte
	ct     string
	status int
	err    error
}

// coverFlightDo 让并发请求共享同一次上游取图。
// 与章节图的 chapterImageFetchMerged 同构，只是独立成表以免两边的 key 互相干扰。
func coverFlightDo(ctx context.Context, key string, fetch func(context.Context) ([]byte, string, int, error)) ([]byte, string, int, error) {
	coverFlightsMu.Lock()
	if f, ok := coverFlights[key]; ok {
		coverFlightsMu.Unlock()
		select {
		case <-f.done:
			return f.body, f.ct, f.status, f.err
		case <-ctx.Done():
			return nil, "", 0, ctx.Err()
		}
	}
	f := &coverFlight{done: make(chan struct{})}
	coverFlights[key] = f
	coverFlightsMu.Unlock()

	// 领跑者用独立 context：客户端可能先走了，但抓取仍应完成并进缓存，
	// 下一个人来就能直接命中。
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), coverFetchTimeout)
	body, ct, status, err := fetch(fctx)
	cancel()
	f.body, f.ct, f.status, f.err = body, ct, status, err
	close(f.done)

	coverFlightsMu.Lock()
	delete(coverFlights, key)
	coverFlightsMu.Unlock()
	return body, ct, status, err
}

// coverImageDomains 返回可用于取图的 JM 镜像域名。
func coverImageDomains() []string {
	ds := apiClient().ImageDomains()
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		d = strings.TrimSpace(d)
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		d = strings.TrimRight(d, "/")
		if d == "" || !jmImageHostRe.MatchString(strings.ToLower(d)) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// coverHostWidthOf 返回某镜像域名上封面图的已知宽度；未知返回 0。
func coverHostWidthOf(host string) int {
	coverHostWidthMu.Lock()
	defer coverHostWidthMu.Unlock()
	return coverHostWidth[strings.ToLower(host)]
}

func rememberCoverHostWidth(host string, w int) {
	if w <= 0 {
		return
	}
	coverHostWidthMu.Lock()
	coverHostWidth[strings.ToLower(host)] = w
	coverHostWidthMu.Unlock()
}

// coverCand 是一个抓取候选。
type coverCand struct {
	url   string
	host  string
	known int // 已知宽度，0 表示未知
}

// coverCandidates 返回给定 URL 的抓取候选，按「够不够用」排序。
//
// 同一个封面在各镜像域名上都有内容，因此某个域名失效时可以换域名重试。
// 排序上「已知宽度够用」的域名优先：请求 320 宽的封面时，先去小变体
// （13KB，直接透传），而不是先抓 600px 的大变体再缩。
func coverCandidates(u *url.URL, wantW int) []string {
	origHost := strings.ToLower(u.Hostname())
	var cands []coverCand
	seen := map[string]bool{}
	add := func(raw, host string) {
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		cands = append(cands, coverCand{url: raw, host: strings.ToLower(host), known: coverHostWidthOf(host)})
	}
	add(u.String(), origHost)
	if jmImageHostRe.MatchString(origHost) {
		for _, d := range coverImageDomains() {
			if strings.EqualFold(d, origHost) {
				continue
			}
			uu := *u
			uu.Scheme = "https"
			uu.Host = d
			add(uu.String(), d)
		}
	}
	// 够用 > 未知 > 已知但不够用；同档内按宽度择优
	// （够用的挑最小的，变体小＝上游流量小，还可能省掉一次缩放；
	//  不够用的挑最大的，留作兜底）。
	sort.SliceStable(cands, func(i, j int) bool {
		return coverCandLess(cands[i], cands[j], wantW)
	})
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.url)
	}
	return out
}

// coverCandDist 候选宽度与目标宽度的距离。
func coverCandDist(c coverCand, wantW int) int {
	if wantW <= 0 || c.known <= 0 {
		return 1 << 30
	}
	d := c.known - wantW
	if d < 0 {
		d = -d
	}
	return d
}

// coverCandLess 判断候选 a 是否应排在 b 前面。
func coverCandLess(a, b coverCand, wantW int) bool {
	ra, rb := coverCandRank(a, wantW), coverCandRank(b, wantW)
	if ra != rb {
		return ra < rb
	}
	switch ra {
	case 0: // 都刚好：挑最小的，上游流量最小
		return a.known < b.known
	case 2, 3: // 都偏大或都偏小：挑更接近目标的
		return coverCandDist(a, wantW) < coverCandDist(b, wantW)
	}
	return false
}

// coverCandRank 给候选排序打分（越小越优先）。
//
//	0 宽度刚好：原样透传即可，流量与 CPU 都最省
//	1 宽度未知：先试试看
//	2 偏大：要缩放才有意义
//	3 偏小：不够看
func coverCandRank(c coverCand, wantW int) int {
	if wantW <= 0 || c.known <= 0 {
		return 1 // 宽度未知
	}
	switch {
	case c.known < int(float64(wantW)*coverVariantMinRatio):
		return 3 // 偏小
	case c.known <= int(float64(wantW)*coverVariantMaxRatio):
		return 0 // 刚好
	default:
		return 2 // 偏大
	}
}

// fetchImageOnce 向上游发起一次取图请求，受全局并发上限约束。
// 证书链不完整的第三方图源（如哔咔的 CDN）会自动回退到免校验客户端。
//
// opportunistic 为真时（后台预热）：只抢空余额度，抢不到就放弃这一张。
//
// 只取到「响应头」为止，正文交给调用方读：竞速判定以响应头为准——
// 上游挂死时是连接一直没响应头，而上游只是慢的时候响应头照样按时到，
// 这样才不会把正常请求误判成挂死、也不会把上游流量翻倍。
// 返回的 release 必须在正文读完之后调用：并发额度要覆盖整个取图过程。
// 原因很关键——图床是 HTTP/2，Go 的 h2 客户端把同一域名的请求复用到一个
// 连接上，而连接有流控窗口；如果只拿到响应头就把额度还回去、让正文还挂着，
// 未读完的流会越积越多，直到把连接级窗口耗尽：此后每个请求都只发得出
// 响应头、正文永远不来（实测会拖成 7 秒多，页面表现就是「封面一直转圈」）。
func fetchImageOnce(ctx context.Context, raw string, opportunistic bool) (*http.Response, func(), error) {
	if opportunistic {
		select {
		case imgFetchSem <- struct{}{}:
		case <-time.After(coverPrewarmWait):
			return nil, nil, errFetchBusy
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	} else {
		select {
		case imgFetchSem <- struct{}{}:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	// 单个候选域名有自己的超时：挂住就换下一个镜像，而不是一直等着。
	ctx, cancel := context.WithTimeout(ctx, coverCandidateTimeout)
	u, err := url.Parse(raw)
	if err != nil {
		cancel()
		<-imgFetchSem
		return nil, nil, err
	}
	ref := "https://jmcomic.me/"
	if u.Scheme != "" && u.Host != "" {
		ref = u.Scheme + "://" + u.Host + "/"
	}
	do := func(client *http.Client) (*http.Response, error) {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if rerr != nil {
			return nil, rerr
		}
		req.Header.Set("Referer", ref)
		req.Header.Set("User-Agent", chrome120UA)
		return client.Do(req)
	}
	resp, err := do(proxyClientVerify)
	if err != nil {
		resp, err = do(proxyClientNoVerify)
	}
	if err != nil {
		cancel()
		<-imgFetchSem
		return nil, nil, err
	}
	// 响应头已到：正文由调用方读，读完后必须调用返回的 release
	//（取消 ctx + 归还并发额度）。
	release := func() {
		cancel()
		<-imgFetchSem
	}
	return resp, release, nil
}

// coverFetchBody 抓取封面原始字节，返回「够用」的那一张。
//
// 取图是竞速的：先发第一个候选，超过 coverHedgeDelay 还没回响应头就并行发
// 下一个镜像域名顶上，谁先回用谁。上游会随机把连接挂住（实测 24 并发里就有
// 一个卡了 30 秒），不竞速的话，用户看到的就是「封面一直转圈」。
//
// wantW > 0 时优先挑宽度够用的变体（省上游流量，也省掉一次缩放重编码）；
// 都够不着时退回已经抓到的最大的一张。
func coverFetchBody(ctx context.Context, u *url.URL, wantW int, opportunistic bool) ([]byte, string, error) {
	cands := coverCandidates(u, wantW)
	if len(cands) == 0 {
		return nil, "", errFetchStatus
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type fetchResult struct {
		body   []byte
		ct     string
		status int
		err    error
		host   string
	}
	ch := make(chan fetchResult, len(cands))
	var wg sync.WaitGroup
	defer func() {
		// 先取消再等：竞速落败的候选要立刻断掉，不能拖住这次响应。
		cancel()
		wg.Wait()
	}()
	launch := func(cand string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, release, err := fetchImageOnce(ctx, cand, opportunistic)
			if err != nil {
				ch <- fetchResult{err: err}
				return
			}
			// 正文读完（或放弃）才归还并发额度。
			defer release()
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				drainClose(resp)
				ch <- fetchResult{status: resp.StatusCode, err: errFetchStatus}
				return
			}
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, imgThumbMaxBody))
			if rerr != nil {
				ch <- fetchResult{status: resp.StatusCode, err: rerr}
				return
			}
			host := ""
			if cu, perr := url.Parse(cand); perr == nil {
				host = strings.ToLower(cu.Hostname())
			}
			ch <- fetchResult{body: body, ct: resp.Header.Get("Content-Type"), status: resp.StatusCode, host: host}
		}()
	}

	launch(cands[0])
	timer := time.NewTimer(coverHedgeDelay)
	defer timer.Stop()

	launched, inflight := 1, 1
	for {
		select {
		case r := <-ch:
			inflight--
			switch {
			case r.err != nil || r.status != http.StatusOK:
				if errors.Is(r.err, errFetchBusy) {
					// 预热没抢到额度：这轮不跟用户抢，直接放弃。
					return nil, "", errFetchBusy
				}
			case len(r.body) == 0:
				// 上游对不存在的资源会回 200 空体：直接原样回发
				// （与旧行为一致），不去等其它镜像域名。
				return r.body, r.ct, nil
			default:
				w := 0
				if cfg, _, derr := image.DecodeConfig(bytes.NewReader(r.body)); derr == nil {
					w = cfg.Width
					rememberCoverHostWidth(r.host, w)
				}
				// 宽度未知：原样回发。
				if wantW <= 0 || w == 0 {
					scheduleVariantProbe(u)
					return r.body, r.ct, nil
				}
				// 宽度刚好：原样透传，流量与 CPU 都最省。
				if w >= int(float64(wantW)*coverVariantMinRatio) && w <= int(float64(wantW)*coverVariantMaxRatio) {
					scheduleVariantProbe(u)
					return r.body, r.ct, nil
				}
				// 偏大或偏小：直接用这张。合适的变体靠「记住各镜像的分辨率」
				// 来提前选中（见 scheduleVariantProbe），不靠在这里干等——
				// 干等意味着每一张图都多抓一次，流量和 CPU 都翻倍。
				scheduleVariantProbe(u)
				return r.body, r.ct, nil
			}
		case <-timer.C:
			// 前面那个连响应头都没回：发下一个镜像域名顶上。
			if launched < len(cands) {
				launch(cands[launched])
				launched++
				inflight++
				timer.Reset(coverHedgeDelay)
			} else {
				// 都发完了，等它们自己超时。
				timer.Reset(coverCandidateTimeout)
			}
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
		if inflight == 0 && launched == len(cands) {
			break
		}
	}
	return nil, "", errFetchStatus
}

// scheduleVariantProbe 后台去试一次其它镜像域名，把它们的分辨率记下来。
//
// 各镜像给的封面分辨率并不一样（306px 的小变体 / 600px 的大变体），
// 但这个事实只有抓一次才知道。记下来之后，挑变体就不用再靠试错：
// 请求 320 宽的封面时直接去小变体（13KB、可直接透传），
// 而不是先拉 600px 的大变体再缩一遍。
//
// 每个域名只探一次，代价可以忽略。
func scheduleVariantProbe(u *url.URL) {
	if u == nil {
		return
	}
	host := strings.ToLower(u.Hostname())
	if !jmImageHostRe.MatchString(host) {
		return
	}
	// 只探列表里的首个域名：实测小变体（306px）就出现在那里，
	// 探一个就够把「挑变体」的判断建立起来，不必把每个镜像都抓一遍。
	ds := coverImageDomains()
	if len(ds) == 0 {
		return
	}
	d := ds[0]
	if strings.EqualFold(d, host) {
		return
	}
	coverProbeMu.Lock()
	known := coverProbed[d] || coverHostWidthOf(d) > 0
	if !known {
		coverProbed[d] = true
	}
	coverProbeMu.Unlock()
	if known {
		return
	}
	pu, err := url.Parse("https://" + d + u.Path)
	if err != nil {
		return
	}
	go func(pu *url.URL) {
		ctx, cancel := context.WithTimeout(context.Background(), coverCandidateTimeout+2*time.Second)
		defer cancel()
		// wantW=0：拿到就够，coverFetchBody 内部会记下该域名的分辨率。
		_, _, _ = coverFetchBody(ctx, pu, 0, true)
	}(pu)
}

// coverItem 是一条待预热的封面。
type coverItem struct {
	URL   string
	Width int
}

// fillCoverImages 给列表数据补齐封面地址，并返回待预热的封面。
//
// 上游 /promote 的 image 字段常常是空的，前端只能自己拼一个硬编码域名
// （cdn-msp.jmapiproxy2.cc）。那个域名给的是 600×600 的大变体，服务端
// 每次都要下载 31KB、解码整幅图再缩到 320；而 cdn-msp.jmapiproxy1.cc 给的
// 是 306×306 的小变体（13KB），直接透传即可。
// 由服务端补地址有三个好处：域名只此一处、用的是省一半流量的变体、
// 预热用的 URL 与前端请求的 URL 完全一致（否则缓存键对不上，预热白做）。
func fillCoverImages(data any) []coverItem {
	sections, ok := data.([]any)
	if !ok {
		return nil
	}
	base := imgBase()
	if base == "" {
		return nil
	}
	var items []coverItem
	for _, sec := range sections {
		sm, ok := sec.(map[string]any)
		if !ok {
			continue
		}
		content, _ := sm["content"].([]any)
		for _, it := range content {
			im, ok := it.(map[string]any)
			if !ok {
				continue
			}
			image, _ := im["image"].(string)
			switch {
			case image == "":
				id := coverItemID(im)
				if id == "" {
					continue
				}
				image = base + "/media/albums/" + id + ".jpg"
				im["image"] = image
			case strings.HasPrefix(image, "/"):
				if strings.Contains(image, "/media/library/") {
					// 书库路径在各镜像域名上都取不到，前端会显示占位图，这里也不预热。
					continue
				}
				image = base + image
				im["image"] = image
			default:
				continue
			}
			items = append(items, coverItem{URL: image, Width: 320})
		}
	}
	return items
}

// coverItemID 取条目 id（上游有时给字符串、有时给数字）。
func coverItemID(im map[string]any) string {
	switch v := im["id"].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	}
	return ""
}

// prewarmEnabled 预热是否开启。
//
// 默认关闭：预热会把整页 189 张封面都抓一遍（比用户自己会加载的还多），
// 实测让服务端 CPU 涨到 2.8 倍，而上游带宽才是真正的瓶颈——预热抢不到带宽，
// 反而拖慢用户正在等的那几张。需要时用 JM_AURA_PREWARM=1 打开。
var prewarmEnabled = os.Getenv("JM_AURA_PREWARM") == "1"

// prewarmCovers 后台预热封面缩略图。
//
// 列表接口返回后稍作等待再开始，用户滚动到某一行时封面通常已经在缓存里，
// 命中即返回（亚毫秒），而不是等用户滚到了才开始抓。
//
// 预热是尽力而为：抢不到上游额度就跳过这一张，绝不占用用户请求的额度。
func prewarmCovers(items []coverItem) {
	if !prewarmEnabled || len(items) == 0 {
		return
	}
	if len(items) > coverPrewarmLimit {
		items = items[:coverPrewarmLimit]
	}
	go func() {
		time.Sleep(coverPrewarmDelay)
		sem := make(chan struct{}, coverPrewarmConc)
		// 从列表尾部往前预热：用户是自上而下滚的，越靠后的内容他越晚才需要，
		// 先把它们备好，等他滚到时就是瞬时显示。
		for i := len(items) - 1; i >= 0; i-- {
			it := items[i]
			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()
				prewarmOne(it)
			}()
		}
	}()
}

// prewarmOne 预热单张封面。
func prewarmOne(it coverItem) {
	if it.URL == "" || it.Width <= 0 {
		return
	}
	key := strconv.Itoa(it.Width) + "|" + it.URL
	if _, _, hit := imgThumbs.get(key); hit {
		return
	}
	u, err := url.Parse(it.URL)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), coverFetchTimeout)
	defer cancel()
	// 预热走独立路径（不占 single-flight）：用户请求若正好要这张图，
	// 两条路各自抓一次，代价只是偶尔多一次上游请求；
	// 换来的是预热永远不会因为「额度被占满」而拖住用户的请求。
	body, ct, err := coverFetchBody(ctx, u, it.Width, true)
	if err != nil || len(body) == 0 {
		if errors.Is(err, errFetchBusy) {
			// 上游正忙：歇一会儿再试，别空转。
			time.Sleep(coverPrewarmBackoff)
		}
		return
	}
	if coverNeedsScale(body, it.Width) {
		if out, oct, ok := imgDownscale(body, it.Width); ok {
			imgThumbs.put(key, out, oct)
			return
		}
	}
	// 本来就够小：原样缓存（省掉一次解码 + 重编码）。
	imgThumbs.put(key, body, ct)
}

// coverNeedsScale 判断这张封面要不要缩放。
//
// 源图不比目标大就不用缩——原样透传既省掉一次整幅解码 + 重编码
// （纯 Go 解 WebP 要 100~300ms CPU，这台部署机只有 2 核），
// 也不会把更大的图发给客户端。
func coverNeedsScale(body []byte, targetW int) bool {
	if targetW <= 0 {
		return false
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return false
	}
	return cfg.Width > targetW
}

// coverETag 计算内容的 ETag（用于条件请求）。
func coverETag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}
