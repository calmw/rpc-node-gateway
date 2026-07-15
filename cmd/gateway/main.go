package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cisco/rpc-node-gateway/internal/auth"
	"github.com/cisco/rpc-node-gateway/internal/balancer"
	"github.com/cisco/rpc-node-gateway/internal/billing"
	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/middleware"
	"github.com/cisco/rpc-node-gateway/internal/proxy"
	"github.com/cisco/rpc-node-gateway/internal/ratelimit"
	"github.com/cisco/rpc-node-gateway/internal/store"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	rdb, err := store.NewRedis(cfg.Redis)
	if err != nil {
		slog.Error("init redis failed", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()

	tokenStore, err := auth.NewStore(cfg)
	if err != nil {
		slog.Error("init token store failed", "err", err)
		os.Exit(1)
	}

	limiter := ratelimit.New(rdb, middleware.LogRateLimitReject)
	bill := billing.NewPublisher(cfg.Billing, rdb)
	defer bill.Close()

	registry := balancer.NewRegistry(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.StartHealthChecks(ctx, cfg.HealthCheck.Interval)

	proxyHandler := proxy.NewHandler(registry, limiter, bill)

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(cfg.Server.WriteTimeout))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	pathAuth := middleware.PathAuth{Tokens: tokenStore}
	rateMW := middleware.RateLimit{Limiter: limiter}
	methodMW := middleware.MethodFilter{}

	// http://{domain}/{token}/{chain}（域名校验不作用于 /healthz）
	r.Group(func(r chi.Router) {
		r.Use(middleware.Domain{Domains: cfg.Server.Domains}.Middleware)
		for chainID := range cfg.Chains {
			id := chainID
			route := "/{token}/" + id
			r.With(
				pathAuth.Middleware,
				middleware.Chain{ChainID: id}.Middleware,
				methodMW.Middleware,
				rateMW.Middleware,
			).Post(route, proxyHandler.ServeHTTP)
			slog.Info("route registered", "chain", id, "path", "/{token}/"+id, "nodes", len(cfg.Chains[id].Nodes))
		}
	})

	srv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout + 5*time.Second,
	}

	go func() {
		slog.Info("gateway listening", "addr", cfg.Server.Addr, "domains", cfg.Server.Domains, "tls", false)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}
