package app

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func gzipBytes(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatalf("gzip 写入失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip 关闭失败: %v", err)
	}
	return buf.Bytes()
}

// TestServePrecompressedGzip 预压缩的 gzip 变体要能直接下发，且解压后与原文一致。
func TestServePrecompressedGzip(t *testing.T) {
	raw := []byte("console.log('aura')")
	gz := gzipBytes(t, raw)
	sub := fstest.MapFS{
		"assets/app.js":    &fstest.MapFile{Data: raw},
		"assets/app.js.gz": &fstest.MapFile{Data: gz},
	}

	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	if !servePrecompressed(rec, req, sub, "assets/app.js") {
		t.Fatal("应该命中 gzip 预压缩变体")
	}
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, 期望 gzip", got)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("Content-Type = %q, 期望 javascript", ct)
	}
	if v := res.Header.Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Fatalf("Vary = %q, 期望包含 Accept-Encoding", v)
	}
	if cl := res.Header.Get("Content-Length"); cl != strconv.Itoa(len(gz)) {
		t.Fatalf("Content-Length = %q, 期望 %d", cl, len(gz))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("响应体不是合法 gzip: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("读取 gzip 响应体失败: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("解压后内容与原文不一致")
	}
}

// TestServePrecompressedBrotliOnlyOnHTTPS brotli 只在安全上下文下发，
// 明文连接必须退回 gzip，避免老版本 Safari 拿到不认识的编码。
func TestServePrecompressedBrotliOnlyOnHTTPS(t *testing.T) {
	raw := []byte("body{color:red}")
	gz := gzipBytes(t, raw)
	br := []byte("pretend-brotli-bytes")
	sub := fstest.MapFS{
		"assets/a.css":    &fstest.MapFile{Data: raw},
		"assets/a.css.gz": &fstest.MapFile{Data: gz},
		"assets/a.css.br": &fstest.MapFile{Data: br},
	}

	// 明文：只应拿到 gzip
	req := httptest.NewRequest("GET", "/assets/a.css", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := httptest.NewRecorder()
	if !servePrecompressed(rec, req, sub, "assets/a.css") {
		t.Fatal("应该命中预压缩变体")
	}
	if got := rec.Result().Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("明文连接的 Content-Encoding = %q, 期望 gzip", got)
	}

	// HTTPS：优先 brotli
	req = httptest.NewRequest("GET", "https://example.com/assets/a.css", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec = httptest.NewRecorder()
	if !servePrecompressed(rec, req, sub, "assets/a.css") {
		t.Fatal("应该命中预压缩变体")
	}
	res := rec.Result()
	if got := res.Header.Get("Content-Encoding"); got != "br" {
		t.Fatalf("HTTPS 连接的 Content-Encoding = %q, 期望 br", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), br) {
		t.Fatalf("brotli 响应体与预压缩文件不一致")
	}

	// 反向代理后面（X-Forwarded-Proto: https）同样视为安全上下文
	req = httptest.NewRequest("GET", "http://example.com/assets/a.css", nil)
	req.Header.Set("Accept-Encoding", "br")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	servePrecompressed(rec, req, sub, "assets/a.css")
	if got := rec.Result().Header.Get("Content-Encoding"); got != "br" {
		t.Fatalf("反代后的 Content-Encoding = %q, 期望 br", got)
	}
}

// TestServePrecompressedFallsBack 没有预压缩产物、或客户端都不支持时，
// 必须回落到原始文件（由调用方继续走普通静态服务）。
func TestServePrecompressedFallsBack(t *testing.T) {
	raw := []byte("console.log(1)")
	sub := fstest.MapFS{
		"assets/app.js": &fstest.MapFile{Data: raw},
	}

	// 没有任何预压缩产物
	req := httptest.NewRequest("GET", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	if servePrecompressed(httptest.NewRecorder(), req, sub, "assets/app.js") {
		t.Fatal("没有预压缩产物时不该命中")
	}

	// 客户端不支持任何压缩
	sub["assets/app.js.gz"] = &fstest.MapFile{Data: gzipBytes(t, raw)}
	req = httptest.NewRequest("GET", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "identity")
	if servePrecompressed(httptest.NewRecorder(), req, sub, "assets/app.js") {
		t.Fatal("客户端不支持压缩时不该命中")
	}

	// 二进制资源不参与预压缩
	req = httptest.NewRequest("GET", "/assets/a.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	if servePrecompressed(httptest.NewRecorder(), req, sub, "assets/a.png") {
		t.Fatal("图片类资源不该走预压缩")
	}
}

// TestServePrecompressedHeadRequest HEAD 请求不能带响应体，但要有 Content-Length。
func TestServePrecompressedHeadRequest(t *testing.T) {
	raw := []byte("console.log(1)")
	gz := gzipBytes(t, raw)
	sub := fstest.MapFS{
		"assets/app.js":    &fstest.MapFile{Data: raw},
		"assets/app.js.gz": &fstest.MapFile{Data: gz},
	}
	req := httptest.NewRequest("HEAD", "/assets/app.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	if !servePrecompressed(rec, req, sub, "assets/app.js") {
		t.Fatal("HEAD 请求也应该命中预压缩变体")
	}
	res := rec.Result()
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD 响应不应带响应体，实际 %d 字节", rec.Body.Len())
	}
	if cl := res.Header.Get("Content-Length"); cl != strconv.Itoa(len(gz)) {
		t.Fatalf("Content-Length = %q, 期望 %d", cl, len(gz))
	}
}

// firstEmbeddedAsset 从内嵌的前端产物里找一个 JS 资源。
func firstEmbeddedAsset(t *testing.T) (string, []byte) {
	t.Helper()
	var name string
	var data []byte
	_ = fs.WalkDir(webDistFS, "webdist", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(p, "webdist/assets/") && strings.HasSuffix(p, ".js") {
			b, err := webDistFS.ReadFile(p)
			if err != nil {
				return err
			}
			name = strings.TrimPrefix(p, "webdist/")
			data = b
			return fs.SkipAll
		}
		return nil
	})
	if name == "" {
		t.Skip("没有内嵌的前端产物，跳过")
	}
	return name, data
}

// TestStaticAssetNegotiationThroughRouter 走完整路由链（Wrap → NewRouter），
// 校验静态资源的内容协商：支持压缩的客户端拿到压缩内容，
// 不支持的拿到原文，两边都要带 Vary: Accept-Encoding。
func TestStaticAssetNegotiationThroughRouter(t *testing.T) {
	name, raw := firstEmbeddedAsset(t)
	h := Wrap(NewRouter())

	t.Run("gzip", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/"+name, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		res := rec.Result()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("状态码 = %d, 期望 200", res.StatusCode)
		}
		if v := res.Header.Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
			t.Fatalf("Vary = %q, 期望包含 Accept-Encoding", v)
		}
		if enc := res.Header.Get("Content-Encoding"); enc != "gzip" {
			t.Fatalf("Content-Encoding = %q, 期望 gzip", enc)
		}
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("响应体不是合法 gzip: %v", err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("读取 gzip 响应体失败: %v", err)
		}
		if !bytes.Equal(got, raw) {
			t.Fatalf("解压后内容与内嵌文件不一致")
		}
	})

	t.Run("identity", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/"+name, nil)
		req.Header.Set("Accept-Encoding", "identity")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		res := rec.Result()
		if enc := res.Header.Get("Content-Encoding"); enc != "" {
			t.Fatalf("Content-Encoding = %q, 期望空", enc)
		}
		if !bytes.Equal(rec.Body.Bytes(), raw) {
			t.Fatalf("identity 响应体与内嵌文件不一致")
		}
		if v := res.Header.Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
			t.Fatalf("Vary = %q, 期望包含 Accept-Encoding", v)
		}
	})

	// 「gzip;q=0」表示明确不接受 gzip，不能被当成接受
	t.Run("gzip-q-zero", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/"+name, nil)
		req.Header.Set("Accept-Encoding", "gzip;q=0")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		res := rec.Result()
		if enc := res.Header.Get("Content-Encoding"); enc != "" {
			t.Fatalf("Content-Encoding = %q, 期望空（q=0 表示不接受）", enc)
		}
		if !bytes.Equal(rec.Body.Bytes(), raw) {
			t.Fatalf("q=0 时响应体应与内嵌文件一致")
		}
	})
}
