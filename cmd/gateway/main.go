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

	"github.com/cisco/rpc-node-gateway/internal/admin"
	"github.com/cisco/rpc-node-gateway/internal/adminjwt"
	"github.com/cisco/rpc-node-gateway/internal/auth"
	"github.com/cisco/rpc-node-gateway/internal/balancer"
	"github.com/cisco/rpc-node-gateway/internal/billing"
	"github.com/cisco/rpc-node-gateway/internal/config"
	"github.com/cisco/rpc-node-gateway/internal/db"
	"github.com/cisco/rpc-node-gateway/internal/middleware"
	"github.com/cisco/rpc-node-gateway/internal/proxy"
	"github.com/cisco/rpc-node-gateway/internal/ratelimit"
	"github.com/cisco/rpc-node-gateway/internal/stats"
	"github.com/cisco/rpc-node-gateway/internal/store"
	"github.com/cisco/rpc-node-gateway/internal/wsproxy"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rdb, err := store.NewRedis(cfg.Redis)
	if err != nil {
		slog.Error("init redis failed", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()

	var pg *db.Postgres
	if cfg.Database.Enabled {
		pg, err = db.Connect(ctx, cfg.Database)
		if err != nil {
			slog.Error("init database failed", "err", err)
			os.Exit(1)
		}
		defer pg.Close()
		slog.Info("database connected")
	}

	tokenStore, err := initTokenStore(ctx, cfg, pg)
	if err != nil {
		slog.Error("init token store failed", "err", err)
		os.Exit(1)
	}

	collector := stats.NewCollector()
	collector.RegisterTokens(tokenStore.ListMeta())

	limiter := ratelimit.New(rdb, middleware.LogRateLimitReject)
	bill := billing.NewPublisher(billing.Options{
		Config: cfg.Billing,
		Redis:  rdb,
		DB:     pg,
	})
	defer bill.Close()

	registry := balancer.NewRegistry(cfg)
	registry.StartHealthChecks(ctx, cfg.HealthCheck.Interval)

	proxyHandler := proxy.NewHandler(registry, limiter, bill, collector)
	var wsHandler *wsproxy.Handler
	if cfg.WebSocket.Enabled {
		wsHandler = wsproxy.NewHandler(registry, limiter, bill, collector, cfg.WebSocket)
	}

	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	// 注意：不要对 WS 使用全局 Timeout，长连接会被掐断

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	if cfg.Admin.Enabled {
		jwtStore := initAdminJWTStore(pg)
		issuer := adminjwt.Issuer{
			Secret:  []byte(cfg.Admin.JWT.Secret),
			Issuer:  cfg.Admin.JWT.Issuer,
			Subject: cfg.Admin.JWT.Subject,
			TTL:     cfg.Admin.JWT.TTL,
		}
		validator := &adminjwt.Validator{
			Secret:  []byte(cfg.Admin.JWT.Secret),
			Issuer:  cfg.Admin.JWT.Issuer,
			Subject: cfg.Admin.JWT.Subject,
			Store:   jwtStore,
		}
		rotator := &adminjwt.Rotator{
			Issuer: issuer,
			Store:  jwtStore,
			Cfg: adminjwt.RotatorConfig{
				Every:            cfg.Admin.JWT.RotateEvery,
				OnStart:          cfg.Admin.JWT.RotateOnStart,
				RevokePrevious:   cfg.Admin.JWT.RevokePrevious,
				CleanupRetention: cfg.Admin.JWT.CleanupRetention,
				LogToken:         cfg.Admin.JWT.LogToken,
			},
		}
		rotator.Start(ctx)

		admin.Handler{
			Stats:      collector,
			Tokens:     tokenStore,
			Validator:  validator,
			Registry:   registry,
			ConfigPath: *cfgPath,
		}.Routes(r)
		slog.Info("admin stats api enabled (jwt)",
			"paths", []string{"/admin/stats", "/admin/upstreams", "/admin/upstreams/reload"},
			"rotate_every", cfg.Admin.JWT.RotateEvery.String(),
			"ttl", cfg.Admin.JWT.TTL.String(),
		)
	}

	pathAuth := middleware.PathAuth{Tokens: tokenStore, Stats: collector}
	rateMW := middleware.RateLimit{Limiter: limiter, Stats: collector}
	methodMW := middleware.MethodFilter{Stats: collector}

	r.Group(func(r chi.Router) {
		r.Use(middleware.Domain{Domains: cfg.Server.Domains, Stats: collector}.Middleware)
		for chainID := range cfg.Chains {
			id := chainID
			route := "/{token}/" + id

			// HTTP JSON-RPC
			r.With(
				chimw.Timeout(cfg.Server.WriteTimeout),
				pathAuth.Middleware,
				middleware.Chain{ChainID: id}.Middleware,
				methodMW.Middleware,
				rateMW.Middleware,
			).Post(route, proxyHandler.ServeHTTP)

			// WebSocket（同路径 Upgrade；限流/方法过滤在消息层）
			if wsHandler != nil {
				r.With(
					pathAuth.Middleware,
					middleware.Chain{ChainID: id}.Middleware,
				).Get(route, wsHandler.ServeHTTP)
			}

			slog.Info("route registered", "chain", id, "path", "/{token}/"+id, "nodes", len(cfg.Chains[id].Nodes), "ws", cfg.WebSocket.Enabled)
		}
	})

	srv := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout + 5*time.Second,
	}

	go func() {
		slog.Info("gateway listening",
			"addr", cfg.Server.Addr,
			"domains", cfg.Server.Domains,
			"database", cfg.Database.Enabled,
			"tokens", tokenStore.Len(),
			"chains", registry.ChainIDs(),
			"tls", false,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	// SIGHUP：热更新各网络节点列表（不增删网络 / 不改路由）
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for sig := range sigCh {
		if sig == syscall.SIGHUP {
			slog.Info("SIGHUP received, reloading chain nodes")
			newCfg, err := config.Load(*cfgPath)
			if err != nil {
				slog.Error("reload config failed", "err", err)
				continue
			}
			if err := registry.ReloadNodes(newCfg.Chains); err != nil {
				slog.Error("reload nodes failed", "err", err)
				continue
			}
			continue
		}
		slog.Info("shutting down", "signal", sig.String())
		break
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

func initTokenStore(ctx context.Context, cfg *config.Config, pg *db.Postgres) (*auth.Store, error) {
	if cfg.Database.Enabled && pg != nil {
		return auth.NewStoreFromDB(ctx, pg, cfg.Database.TokenRefresh)
	}
	return auth.NewStoreFromConfig(cfg)
}

func initAdminJWTStore(pg *db.Postgres) adminjwt.Store {
	if pg != nil {
		slog.Info("admin jwt store: postgres")
		return adminjwt.NewPostgresStore(pg)
	}
	slog.Warn("admin jwt store: memory (enable database to persist tokens)")
	return adminjwt.NewMemoryStore()
}
