package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"game-gateway/internal/jsoncfg"
	"game-gateway/internal/mono"
)

func main() {
	configPath := flag.String("config", "configs/mono.json", "mono config file path")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load mono config", "config_path", *configPath, "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv, err := mono.New(cfg, logger)
	if err != nil {
		logger.Error("mono init failed", "error", err)
		os.Exit(1)
	}
	go func() {
		if err := srv.Start(); err != nil {
			logger.Error("mono server error", "error", err)
			os.Exit(1)
		}
	}()
	logPageURLs(logger, cfg.ListenAddr)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = srv.Stop(ctx)
}

type fileConfig struct {
	ListenAddr       string `json:"listen_addr"`
	DBPath           string `json:"db_path"`
	TokenTTL         string `json:"token_ttl"`
	HeartbeatTimeout string `json:"heartbeat_timeout"`
	ShutdownTimeout  string `json:"shutdown_timeout"`
}

func loadConfig(path string) (mono.Config, error) {
	raw := fileConfig{
		ListenAddr:       ":8099",
		DBPath:           "./mono.db",
		TokenTTL:         "24h",
		HeartbeatTimeout: "5s",
		ShutdownTimeout:  "10s",
	}
	if err := jsoncfg.Load(path, &raw); err != nil {
		return mono.Config{}, err
	}
	tokenTTL, err := time.ParseDuration(raw.TokenTTL)
	if err != nil {
		return mono.Config{}, err
	}
	heartbeatTimeout, err := time.ParseDuration(raw.HeartbeatTimeout)
	if err != nil {
		return mono.Config{}, err
	}
	shutdownTimeout, err := time.ParseDuration(raw.ShutdownTimeout)
	if err != nil {
		return mono.Config{}, err
	}
	return mono.Config{
		ListenAddr:       raw.ListenAddr,
		DBPath:           raw.DBPath,
		TokenTTL:         tokenTTL,
		HeartbeatTimeout: heartbeatTimeout,
		ShutdownTimeout:  shutdownTimeout,
	}, nil
}

func logPageURLs(logger *slog.Logger, listenAddr string) {
	base := buildBaseURL(listenAddr)
	fmt.Println("==== MONO 页面地址 ====")
	fmt.Println("登录页:", base+"/")
	fmt.Println("大厅页:", base+"/lobby")
	fmt.Println("房间页:", base+"/room")
	fmt.Println("游戏页:", base+"/game")
	fmt.Println("健康检查:", base+"/healthz")
	logger.Info("mono pages",
		"login", base+"/",
		"lobby", base+"/lobby",
		"room", base+"/room",
		"game", base+"/game",
		"healthz", base+"/healthz",
	)
}

func buildBaseURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		if strings.HasPrefix(listenAddr, ":") {
			host = ""
			port = strings.TrimPrefix(listenAddr, ":")
		} else {
			return "http://" + listenAddr
		}
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s", strings.Trim(host, "[]"), port)
}
