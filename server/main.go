package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"jmaura/internal/app"
)

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// resolveHost 只接受 IP 字面量作为监听地址。
// 容器 / PaaS（如 Zeabur）里常把 JM_AURA_HOST 误设成服务的内部主机名，
// 那样 net.Listen 会去解析 DNS，失败后 ListenAndServe 返回错误、main 直接
// log.Fatal，容器就会反复重启（BackOff）。这里退回到 0.0.0.0，只告警不致命。
func resolveHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "0.0.0.0"
	}
	if net.ParseIP(raw) == nil {
		log.Printf("警告: JM_AURA_HOST=%q 不是 IP 字面量，已回退到 0.0.0.0（容器内应监听 0.0.0.0）", raw)
		return "0.0.0.0"
	}
	return raw
}

func main() {
	host := resolveHost(envOr("JM_AURA_HOST", "0.0.0.0"))
	port := envOr("JM_AURA_PORT", envOr("PORT", "8000"))
	addr := host + ":" + port
	log.Printf("JM-Aura server listening on http://%s", addr)

	// 后台预热与上游的连接（API + 图床各一次），让冷启动后的第一批请求
	// 不必逐个付出 TCP/TLS 握手成本。失败只记日志，不影响服务可用性。
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("预热上游连接时出错: %v", rec)
			}
		}()
		app.WarmupUpstream(context.Background())
		log.Printf("上游连接预热完成")
	}()

	if err := http.ListenAndServe(addr, app.Wrap(app.NewRouter())); err != nil {
		log.Fatal(err)
	}
}
