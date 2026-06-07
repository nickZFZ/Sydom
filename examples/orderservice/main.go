package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"

	oapp "github.com/nickZFZ/Sydom/examples/orderservice/app"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() { os.Exit(run()) }

func run() int {
	dsn := getenv("DATABASE_DSN", "")
	sidecar := getenv("SIDECAR_ADDR", "127.0.0.1:8090")
	listen := getenv("LISTEN_ADDR", ":8080")
	if dsn == "" {
		log.Print("DATABASE_DSN 必填")
		return 1
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("open db: %v", err)
		return 1
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client, err := sydom.New(sidecar)
	if err != nil {
		log.Printf("connect sidecar: %v", err)
		return 1
	}
	defer client.Close()

	// 启动时上报权限点目录（fail-soft）。
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	oapp.ReportCatalog(rctx, client)
	cancel()

	h, err := oapp.New(ctx, db, client)
	if err != nil {
		log.Printf("build handler: %v", err)
		return 1
	}

	srv := &http.Server{Addr: listen, Handler: h}
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("订单服务监听 %s（sidecar=%s）", listen, sidecar)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("serve: %v", err)
		return 1
	}
	return 0
}
