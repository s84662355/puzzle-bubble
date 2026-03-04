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
	"game-gateway/internal/ui"
)

func main() {
	configPath := flag.String("config", "configs/ui.json", "ui config file path")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load ui config", "config_path", *configPath, "error", err)
		os.Exit(1)
	}
	if err := ui.ValidateConfig(cfg); err != nil {
		panic(err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv, err := ui.NewServer(cfg, logger)
	if err != nil {
		logger.Error("ui server init failed", "error", err)
		os.Exit(1)
	}

	go func() {
		if err := srv.Start(); err != nil {
			logger.Error("ui server error", "error", err)
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
	ListenAddr          string `json:"listen_addr"`
	AuthGRPCAddr        string `json:"auth_grpc_addr"`
	GameControlAddr     string `json:"game_control_grpc_addr"`
	RoomGRPCAddr        string `json:"room_grpc_addr"`
	GatewayPushGRPCAddr string `json:"gateway_push_grpc_addr"`
	ShutdownTimeout     string `json:"shutdown_timeout"`
}

func loadConfig(path string) (ui.Config, error) {
	raw := fileConfig{
		ListenAddr:          ":8088",
		AuthGRPCAddr:        "127.0.0.1:19090",
		GameControlAddr:     "127.0.0.1:19400",
		RoomGRPCAddr:        "127.0.0.1:19400",
		GatewayPushGRPCAddr: "127.0.0.1:18080",
		ShutdownTimeout:     "10s",
	}
	if err := jsoncfg.Load(path, &raw); err != nil {
		return ui.Config{}, err
	}
	shutdownTimeout, err := time.ParseDuration(raw.ShutdownTimeout)
	if err != nil {
		return ui.Config{}, err
	}
	return ui.Config{
		ListenAddr:          raw.ListenAddr,
		AuthGRPCAddr:        raw.AuthGRPCAddr,
		GameControlAddr:     raw.GameControlAddr,
		RoomGRPCAddr:        raw.RoomGRPCAddr,
		GatewayPushGRPCAddr: raw.GatewayPushGRPCAddr,
		ShutdownTimeout:     shutdownTimeout,
	}, nil
}

func logPageURLs(logger *slog.Logger, listenAddr string) {
	base := buildBaseURL(listenAddr)
	fmt.Println("==== UI 页面地址 ====")
	fmt.Println("登录页:", base+"/")
	fmt.Println("大厅页:", base+"/lobby")
	fmt.Println("房间页:", base+"/room")
	fmt.Println("游戏页:", base+"/game")
	logger.Info("ui pages",
		"login", base+"/",
		"lobby", base+"/lobby",
		"room", base+"/room",
		"game", base+"/game",
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
