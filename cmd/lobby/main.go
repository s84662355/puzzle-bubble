package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"game-gateway/internal/bizstub"
	"game-gateway/internal/jsoncfg"
)

func main() {
	configPath := flag.String("config", "configs/lobby.json", "lobby config file path")
	flag.Parse()
	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load lobby config", "config_path", *configPath, "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	srv := bizstub.NewServer("lobby.LobbyService", cfg.ListenAddr, "/lobby.LobbyService/HandleGatewayMessage", logger)
	go func() {
		if err := srv.Start(); err != nil {
			logger.Error("lobby service error", "error", err)
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
}

type config struct {
	ListenAddr      string
	ShutdownTimeout time.Duration
}

func loadConfig(path string) (config, error) {
	raw := fileConfig{
		ListenAddr:      ":19100",
		ShutdownTimeout: "10s",
	}
	if err := jsoncfg.Load(path, &raw); err != nil {
		return config{}, err
	}
	timeout, err := time.ParseDuration(raw.ShutdownTimeout)
	if err != nil {
		return config{}, err
	}
	return config{
		ListenAddr:      raw.ListenAddr,
		ShutdownTimeout: timeout,
	}, nil
}
