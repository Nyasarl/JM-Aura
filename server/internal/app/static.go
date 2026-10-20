package app

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// precompressExts 列出允许预压缩的静态资源扩展名。
// 只处理文本类资源：图片、音视频本身已经是压缩格式，再压一遍纯浪费 CPU。
var precompressExts = map[string]bool{
	".js":          true,
	".mjs":         true,
	".css":         true,
	".html":        true,
	".json":        true,
	".map":         true,
	".txt":         true,
	".xml":         true,
	".webmanifest": true,
	".svg":         true,
}

// servePrecompressed 优先用构建期生成的 .br / .gz 兄弟文件响应静态资源。
//
// 之前所有文本资源都由 gzip 中间件在请求时现场压缩：这台机器只有 2 核，
// 上百人同时访问时压缩 CPU 会成为瓶颈，而传输体积也没有优化空间。
// 预压缩把压缩成本挪到构建期，运行时零 CPU，同时可以用上 brotli。
//
// 返回 true 表示已经写出响应，调用方不应再写。
func servePrecompressed(w http.ResponseWriter, r *http.Request, sub fs.FS, p string) bool {
	if !precompressExts[strings.ToLower(path.Ext(p))] {
		return false
	}
	// brotli 只在 HTTPS 下下发：Safari 只在安全上下文里启用 br，
	// 明文连接下发 br 反而可能让老版本 Safari 直接拒绝响应。
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")

	type variant struct {
		enc    string
		suffix string
	}
	var variants []variant
	if secure && acceptsEncoding(r, "br") {
		variants = append(variants, variant{enc: "br", suffix: ".br"})
	}
	if acceptsEncoding(r, "gzip") {
		variants = append(variants, variant{enc: "gzip", suffix: ".gz"})
	}

	for _, v := range variants {
		f, err := sub.Open(p + v.suffix)
		if err != nil {
			continue // 没有这个预压缩版本，尝试下一种
		}
		st, err := f.Stat()
		if err != nil || st.IsDir() {
			f.Close()
			continue
		}
		hdr := w.Header()
		hdr.Set("Content-Type", contentTypeFor(p, sub))
		hdr.Set("Content-Encoding", v.enc)
		hdr.Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		addVary(hdr, "Accept-Encoding")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, f)
		}
		f.Close()
		return true
	}
	return false
}

// contentTypeFor 推断静态资源的 Content-Type，规则与 http.FileServer 保持一致：
// 先看扩展名，认不出来再嗅探前 512 字节。
func contentTypeFor(p string, sub fs.FS) string {
	if ct := mime.TypeByExtension(path.Ext(p)); ct != "" {
		return ct
	}
	f, err := sub.Open(p)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	var head [512]byte
	n, _ := io.ReadFull(f, head[:])
	return http.DetectContentType(head[:n])
}
