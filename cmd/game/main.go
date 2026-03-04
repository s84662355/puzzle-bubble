package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"game-gateway/internal/gamesvc"
	"game-gateway/internal/jsoncfg"
)

func main() {
	configPath := flag.String("config", "configs/game.json", "game config file path")
	flag.Parse()

	cfg, shutdownTimeout, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load game config", "config_path", *configPath, "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv := gamesvc.New(cfg, logger)

	go func() {
		if err := srv.StartWS(); err != nil {
			logger.Error("game ws server error", "error", err)
			os.Exit(1)
		}
	}()
	go func() {
		if err := srv.StartGRPC(); err != nil {
			logger.Error("game grpc server error", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = srv.Stop(ctx)
}

type fileConfig struct {
	WSListenAddr       string `json:"ws_listen_addr"`
	GRPCListenAddr     string `json:"grpc_listen_addr"`
	PublicWSAddr       string `json:"public_ws_addr"`
	AuthGRPCAddr       string `json:"auth_grpc_addr"`
	TokenVerifyTimeout string `json:"token_verify_timeout"`
	HeartbeatTimeout   string `json:"heartbeat_timeout"`
	RoomFrameTick      string `json:"room_frame_tick"`
	ShutdownTimeout    string `json:"shutdown_timeout"`
}

func loadConfig(path string) (gamesvc.Config, time.Duration, error) {
	raw := fileConfig{
		WSListenAddr:       ":19500",
		GRPCListenAddr:     ":19400",
		PublicWSAddr:       "127.0.0.1:19500",
		AuthGRPCAddr:       "127.0.0.1:19090",
		TokenVerifyTimeout: "2s",
		HeartbeatTimeout:   "5s",
		RoomFrameTick:      "50ms",
		ShutdownTimeout:    "10s",
	}
	if err := jsoncfg.Load(path, &raw); err != nil {
		return gamesvc.Config{}, 0, err
	}
	tokenVerifyTimeout, err := time.ParseDuration(raw.TokenVerifyTimeout)
	if err != nil {
		return gamesvc.Config{}, 0, err
	}
	heartbeatTimeout, err := time.ParseDuration(raw.HeartbeatTimeout)
	if err != nil {
		return gamesvc.Config{}, 0, err
	}
	roomFrameTick, err := time.ParseDuration(raw.RoomFrameTick)
	if err != nil {
		return gamesvc.Config{}, 0, err
	}
	shutdownTimeout, err := time.ParseDuration(raw.ShutdownTimeout)
	if err != nil {
		return gamesvc.Config{}, 0, err
	}
	return gamesvc.Config{
		WSListenAddr:       raw.WSListenAddr,
		GRPCListenAddr:     raw.GRPCListenAddr,
		PublicWSAddr:       raw.PublicWSAddr,
		AuthGRPCAddr:       raw.AuthGRPCAddr,
		TokenVerifyTimeout: tokenVerifyTimeout,
		HeartbeatTimeout:   heartbeatTimeout,
		RoomFrameTick:      roomFrameTick,
	}, shutdownTimeout, nil
}
