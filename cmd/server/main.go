// 日历服务启动入口：装配只读 SQLite、领域服务和 HTTP，并处理优雅退出。
package main

import (
	"context"
	"errors"
	"flag"
	"kcq-calendar-connector/internal/calendar"
	"kcq-calendar-connector/internal/httpapi"
	"kcq-calendar-connector/internal/storage"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// env 从环境读取配置，缺省值只在启动装配中集中定义。
func env(key, defaultValue string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return defaultValue
}

// run 启动服务并在退出信号到来时等待活动请求完成。
func run() error {
	addr := flag.String("addr", env("CALENDAR_ADDR", "127.0.0.1:8094"), "HTTP listen address")
	path := flag.String("db", env("CALENDAR_DB_PATH", "data/calendar.db"), "existing SQLite database")
	origins := flag.String("origins", env("CALENDAR_ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173"), "comma-separated browser origins")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	catalog, err := calendar.NewCatalog()
	if err != nil {
		return err
	}
	store, err := storage.Open(*path)
	if err != nil {
		return err
	}
	defer store.Close()
	server := &http.Server{Addr: *addr, Handler: httpapi.New(calendar.NewService(store, catalog), logger, strings.Split(*origins, ",")), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	logger.Info("calendar server starting", "address", listener.Addr().String(), "database", *path)
	select {
	case err := <-stopped:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// main 将启动失败作为非零进程退出码，便于脚本和服务管理器识别。
func main() {
	if err := run(); err != nil {
		slog.Error("calendar server stopped", "error", err)
		os.Exit(1)
	}
}
