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
	"game-gateway/internal/room"
)

func main() {
	configPath := flag.String("config", "configs/room.json", "room config file path")
	flag.Parse()
	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load room config", "config_path", *configPath, "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	srv := room.NewServer(room.Config{
		ListenAddr:      cfg.ListenAddr,
		ShutdownTimeout: cfg.ShutdownTimeout,
		DefaultCapacity: 2,
		FrameInterval:   cfg.FrameInterval,
	}, logger)
	go func() {
		if err := srv.Start(); err != nil {
			logger.Error("room service error", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = srv.Stop(ctx)
}

type fileConfig struct {
	ListenAddr      string `json:"listen_addr"`
	ShutdownTimeout string `json:"shutdown_timeout"`
	FrameInterval   string `json:"frame_interval"`
}

type config struct {
	ListenAddr      string
	ShutdownTimeout time.Duration
	FrameInterval   time.Duration
}

func loadConfig(path string) (config, error) {
	raw := fileConfig{
		ListenAddr:      ":19300",
		ShutdownTimeout: "10s",
		FrameInterval:   "50ms",
	}
	if err := jsoncfg.Load(path, &raw); err != nil {
		return config{}, err
	}
	shutdownTimeout, err := time.ParseDuration(raw.ShutdownTimeout)
	if err != nil {
		return config{}, err
	}
	frameInterval, err := time.ParseDuration(raw.FrameInterval)
	if err != nil {
		return config{}, err
	}
	return config{
		ListenAddr:      raw.ListenAddr,
		ShutdownTimeout: shutdownTimeout,
		FrameInterval:   frameInterval,
	}, nil
}
