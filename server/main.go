package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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

	srv := &http.Server{
		Addr:    addr,
		Handler: app.Wrap(app.NewRouter()),
		// 读请求头超时：挡掉慢速头攻击（slowloris），否则连接会被长期占住
		ReadHeaderTimeout: 10 * time.Second,
		// 读请求体超时：请求体都很小（没有文件上传），不需要更久
		ReadTimeout: 30 * time.Second,
		// 写超时留空：图片代理与下载是长连接流式响应，写死会误伤慢速客户端
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	// 优雅退出：容器滚动更新时先停止接收新连接，等在途请求处理完再退出，
	// 避免部署瞬间把用户正在用的请求打断成 502。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Printf("收到退出信号，开始优雅关闭")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("优雅关闭超时，强制关闭: %v", err)
			_ = srv.Close()
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("服务已退出")
}
