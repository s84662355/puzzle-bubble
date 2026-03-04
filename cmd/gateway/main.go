package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"game-gateway/internal/config"
	"game-gateway/internal/gateway"
	"game-gateway/internal/pushgrpc"
	"game-gateway/internal/serviceproxy"
	"game-gateway/internal/userclient"
)

func main() {
	configPath := flag.String("config", "configs/gateway.json", "gateway config file path")
	flag.Parse()

	cfg, err := config.LoadFromFile(*configPath)
	if err != nil {
		slog.Error("failed to load gateway config", "config_path", *configPath, "error", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))

	verifier, err := userclient.NewGRPCVerifier(cfg.AuthGRPCAddr)
	if err != nil {
		logger.Error("failed to init auth grpc verifier", "error", err, "auth_grpc_addr", cfg.AuthGRPCAddr)
		os.Exit(1)
	}
	defer verifier.Close()

	dispatcher, err := serviceproxy.NewGRPCDispatcher(cfg)
	if err != nil {
		logger.Error("failed to init service dispatcher", "error", err)
		os.Exit(1)
	}

	tcpGateway, err := gateway.NewTCPGateway(cfg, logger, verifier, dispatcher)
	if err != nil {
		logger.Error("failed to init tcp gateway", "error", err)
		os.Exit(1)
	}

	pushServer, err := pushgrpc.NewServer(cfg, logger, tcpGateway)
	if err != nil {
		logger.Error("failed to init grpc push server", "error", err)
		os.Exit(1)
	}

	go func() {
		logger.Info("tcp gateway starting", "listen_tcp_addr", cfg.ListenTCPAddr)
		if err := tcpGateway.Start(); err != nil {
			logger.Error("tcp gateway error", "error", err)
			os.Exit(1)
		}
	}()
	go func() {
		logger.Info("ws gateway starting", "listen_ws_addr", cfg.WSListenAddr)
		if err := tcpGateway.StartWS(); err != nil {
			logger.Error("ws gateway error", "error", err)
			os.Exit(1)
		}
	}()
	go func() {
		logger.Info("grpc push server starting", "grpc_listen_addr", cfg.GRPCListenAddr)
		if err := pushServer.Start(); err != nil {
			logger.Error("grpc push server error", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := tcpGateway.Stop(shutdownCtx); err != nil {
		logger.Error("gateway shutdown failed", "error", err)
		os.Exit(1)
	}
	if err := pushServer.Stop(shutdownCtx); err != nil {
		logger.Error("grpc push server shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("gateway stopped")
}
