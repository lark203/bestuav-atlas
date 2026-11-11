// Command atlas-sync 是星图的同步服务端：单用户、自建、只做变更的收发与定序。
//
// 它通常跑在一台云主机上（Docker + Caddy 反代），也可以直接在本机跑：
//
//	ATLAS_SYNC_ADMIN_TOKEN=$(openssl rand -hex 16) atlas-sync -db ./data/atlas-sync.db
//
// 管理令牌必须自己给：空值时服务照常接收已登记设备的同步，但**不能**生成配对码
// 或登记新设备——公网实例宁可什么都不做，也不要默认无密码。
//
// 端口默认 7420（避免和 8080 / 3306 这类常见端口撞），只提供 HTTP：
// 暴露到公网时请在前面放 Caddy/Nginx 做 TLS，见 docs 的部署章节。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"bestuav-atlas/internal/syncsvc"
)

// version 由构建脚本注入（-ldflags "-X main.version=..."）；本地构建显示 dev。
var version = "dev"

func main() {
	var (
		listen     = flag.String("listen", envOr("ATLAS_SYNC_LISTEN", ":7420"), "监听地址")
		dbPath     = flag.String("db", envOr("ATLAS_SYNC_DB", filepath.Join("data", "atlas-sync.db")), "服务端数据库路径")
		adminToken = flag.String("admin-token", os.Getenv("ATLAS_SYNC_ADMIN_TOKEN"), "管理员令牌（也可用环境变量）")
		showVer    = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o755); err != nil {
		logger.Error("创建数据目录失败", "err", err, "db", *dbPath)
		os.Exit(1)
	}
	srv, err := syncsvc.Open(*dbPath, *adminToken)
	if err != nil {
		logger.Error("打开数据库失败", "err", err, "db", *dbPath)
		os.Exit(1)
	}
	defer func() { _ = srv.Close() }()
	srv.Version = version

	if *adminToken == "" {
		logger.Warn("未设置管理员令牌：可以接收已登记设备的同步，但无法生成配对码或登记新设备")
	}

	httpSrv := &http.Server{
		Addr:    *listen,
		Handler: srv.Handler(),
		// 同步请求都很小，给足 10 秒即可；读写超时同时防慢速攻击。
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}

	// 优雅退出：容器收到 SIGTERM 时先停止接收新请求，再关库。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("atlas-sync 已启动", "listen", *listen, "db", *dbPath, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		logger.Error("监听失败", "err", err, "listen", *listen)
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("收到退出信号，正在关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("关闭 HTTP 服务失败", "err", err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
