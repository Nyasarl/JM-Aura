package jm

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// 上游连接的共用传输层。
//
// 为什么单独抽出来：上游 API 是 HTTPS，而我们面对的是「200 多人稳定在线」的
// 并发模型。Go 默认的 http.Transport（MaxIdleConnsPerHost=2）在这种并发下几乎
// 每个请求都要重新 TCP + TLS 握手，多花 1~2 个 RTT，并把本机 TIME_WAIT、CPU
// 全消耗在握手上——这也是线上 /album 延迟在 1.2s 和 6.2s 之间乱跳的一个来源。
//
// 另一个坑：Go 的约定是「只要设置了 DialContext / TLSClientConfig 等字段，就保守
// 地不再自动启用 HTTP/2」。上游域名本身支持 h2（实测），所以这里必须显式
// ForceAttemptHTTP2，否则我们会一直用 HTTP/1.1，并发一高就是一串新连接。

const (
	// maxIdleConnsPerHost 要能覆盖同一域名的并发峰值，否则会退化成握手 churn。
	maxIdleConnsPerHost = 48
	maxIdleConns        = 256
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
	dialTimeout         = 5 * time.Second
	// responseHeaderTimeout 是「连接建立后等首字节」的上限。整体仍有
	// requestTimeout 兜底，这里只是让卡死的上游更快让位给故障转移。
	responseHeaderTimeout = 8 * time.Second
	// expectContinueTimeout 保持默认语义，避免大表单请求多等一轮。
	expectContinueTimeout = 1 * time.Second
)

func newTransport(tlsConfig *tls.Config) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true, // 关键：显式兜回被 DialContext/TLSClientConfig 关掉的 h2
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		TLSClientConfig:       tlsConfig,
	}
}

// newAPIHTTPClient 是打 JM API / Web 域名的客户端。
func newAPIHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: newTransport(nil),
	}
}

// 所有 jm.Client（包括登录用的临时 client）共用同一个 API 客户端，
// 这样连接池、DNS 结果、TLS 会话都是共享的，预热也才能惠及全部请求。
var sharedAPIHTTPClient = newAPIHTTPClient()

// Warmup 预热与上游的连接：对每个上游域名各发一次廉价请求，把连接留在
// 传输层空闲池里，避免冷启动/长空闲后的第一批请求全部重新握手。
//
// 失败不影响启动，只是少一次预热，因此这里只做尽力而为。
func (c *Client) Warmup(ctx context.Context) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, warmupTimeout)
	defer cancel()

	targets := append(append([]string(nil), c.APIDomains()...), c.ImageDomains()...)
	if len(targets) == 0 {
		return
	}

	// 并发度不需要太高，域名数量本身是个位数，够快就行。
	const parallel = 6
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, host := range targets {
		host := host
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			warmOne(ctx, c.http, host)
		}()
	}
	wg.Wait()
}

func warmOne(ctx context.Context, hc *http.Client, host string) {
	tctx, cancel := context.WithTimeout(ctx, warmupTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodGet, "https://"+host+"/", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	// 必须把 body 读完并关闭，连接才会被放回空闲池。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
}

const warmupTimeout = 10 * time.Second

// envDuration 读取一个时长格式的环境变量，未设置或非法时返回 0。
// 必须带单位（"8s"、"2500ms"），避免"纯数字是秒还是毫秒"这种笔误级事故。
func envDuration(key string) time.Duration {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}
