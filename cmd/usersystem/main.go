package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"game-gateway/internal/jsoncfg"
	"game-gateway/internal/usersystem"
)

func main() {
	configPath := flag.String("config", "configs/usersystem.json", "usersystem config file path")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load usersystem config", "config_path", *configPath, "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	srv, err := usersystem.NewServer(cfg, logger)
	if err != nil {
		logger.Error("init user system failed", "error", err)
		os.Exit(1)
	}

	go func() {
		if err := srv.StartHTTP(); err != nil {
			logger.Error("user http server error", "error", err)
			os.Exit(1)
		}
	}()
	go func() {
		if err := srv.StartGRPC(); err != nil {
			logger.Error("user grpc server error", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		logger.Error("user service shutdown failed", "error", err)
		os.Exit(1)
	}
}

type fileConfig struct {
	HTTPListenAddr  string `json:"http_listen_addr"`
	GRPCListenAddr  string `json:"grpc_listen_addr"`
	TokenTTL        string `json:"token_ttl"`
	ShutdownTimeout string `json:"shutdown_timeout"`
	RedisAddr       string `json:"redis_addr"`
	RedisPassword   string `json:"redis_password"`
	RedisDB         int    `json:"redis_db"`
	RedisKeyPrefix  string `json:"redis_key_prefix"`
}

func loadConfig(path string) (usersystem.Config, error) {
	raw := fileConfig{
		HTTPListenAddr:  ":19080",
		GRPCListenAddr:  ":19090",
		TokenTTL:        "24h",
		ShutdownTimeout: "10s",
		RedisAddr:       "127.0.0.1:6379",
		RedisKeyPrefix:  "game:token:",
	}
	if err := jsoncfg.Load(path, &raw); err != nil {
		return usersystem.Config{}, err
	}
	tokenTTL, err := time.ParseDuration(raw.TokenTTL)
	if err != nil {
		return usersystem.Config{}, err
	}
	shutdownTimeout, err := time.ParseDuration(raw.ShutdownTimeout)
	if err != nil {
		return usersystem.Config{}, err
	}
	return usersystem.Config{
		HTTPListenAddr:  raw.HTTPListenAddr,
		GRPCListenAddr:  raw.GRPCListenAddr,
		TokenTTL:        tokenTTL,
		ShutdownTimeout: shutdownTimeout,
		RedisAddr:       raw.RedisAddr,
		RedisPassword:   raw.RedisPassword,
		RedisDB:         raw.RedisDB,
		RedisKeyPrefix:  raw.RedisKeyPrefix,
	}, nil
}
