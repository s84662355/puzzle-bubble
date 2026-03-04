package config

import (
	"fmt"
	"log/slog"
	"time"

	"game-gateway/internal/jsoncfg"
)

type Config struct {
	ListenTCPAddr      string
	GRPCListenAddr     string
	WSListenAddr       string
	AuthGRPCAddr       string
	LobbyGRPCAddr      string
	MatchGRPCAddr      string
	RoomGRPCAddr       string
	MaxPacketSize      uint32
	AuthTimeout        time.Duration
	TokenVerifyTimeout time.Duration
	IdleTimeout        time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	ShutdownTimeout    time.Duration
	LogLevel           slog.Level
}

type fileConfig struct {
	ListenTCPAddr      string `json:"listen_tcp_addr"`
	GRPCListenAddr     string `json:"grpc_listen_addr"`
	WSListenAddr       string `json:"ws_listen_addr"`
	AuthGRPCAddr       string `json:"auth_grpc_addr"`
	LobbyGRPCAddr      string `json:"lobby_grpc_addr"`
	MatchGRPCAddr      string `json:"match_grpc_addr"`
	RoomGRPCAddr       string `json:"room_grpc_addr"`
	MaxPacketSize      uint32 `json:"max_packet_size"`
	AuthTimeout        string `json:"auth_timeout"`
	TokenVerifyTimeout string `json:"token_verify_timeout"`
	IdleTimeout        string `json:"idle_timeout"`
	ReadTimeout        string `json:"read_timeout"`
	WriteTimeout       string `json:"write_timeout"`
	ShutdownTimeout    string `json:"shutdown_timeout"`
	LogLevel           string `json:"log_level"`
}

func LoadFromFile(path string) (Config, error) {
	defaultCfg := Config{
		ListenTCPAddr:      ":7000",
		GRPCListenAddr:     ":18080",
		WSListenAddr:       ":18081",
		AuthGRPCAddr:       "127.0.0.1:19090",
		LobbyGRPCAddr:      "127.0.0.1:19100",
		MatchGRPCAddr:      "127.0.0.1:19200",
		RoomGRPCAddr:       "127.0.0.1:19300",
		MaxPacketSize:      64 * 1024,
		AuthTimeout:        10 * time.Second,
		TokenVerifyTimeout: 2 * time.Second,
		IdleTimeout:        90 * time.Second,
		ReadTimeout:        30 * time.Second,
		WriteTimeout:       10 * time.Second,
		ShutdownTimeout:    10 * time.Second,
		LogLevel:           slog.LevelInfo,
	}
	var raw fileConfig
	if err := jsoncfg.Load(path, &raw); err != nil {
		return Config{}, err
	}
	cfg := defaultCfg
	if raw.ListenTCPAddr != "" {
		cfg.ListenTCPAddr = raw.ListenTCPAddr
	}
	if raw.GRPCListenAddr != "" {
		cfg.GRPCListenAddr = raw.GRPCListenAddr
	}
	if raw.WSListenAddr != "" {
		cfg.WSListenAddr = raw.WSListenAddr
	}
	if raw.AuthGRPCAddr != "" {
		cfg.AuthGRPCAddr = raw.AuthGRPCAddr
	}
	if raw.LobbyGRPCAddr != "" {
		cfg.LobbyGRPCAddr = raw.LobbyGRPCAddr
	}
	if raw.MatchGRPCAddr != "" {
		cfg.MatchGRPCAddr = raw.MatchGRPCAddr
	}
	if raw.RoomGRPCAddr != "" {
		cfg.RoomGRPCAddr = raw.RoomGRPCAddr
	}
	if raw.MaxPacketSize > 0 {
		cfg.MaxPacketSize = raw.MaxPacketSize
	}
	parseDuration := func(value string, fallback time.Duration) (time.Duration, error) {
		if value == "" {
			return fallback, nil
		}
		d, err := time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", value, err)
		}
		return d, nil
	}
	var err error
	cfg.AuthTimeout, err = parseDuration(raw.AuthTimeout, cfg.AuthTimeout)
	if err != nil {
		return Config{}, err
	}
	cfg.TokenVerifyTimeout, err = parseDuration(raw.TokenVerifyTimeout, cfg.TokenVerifyTimeout)
	if err != nil {
		return Config{}, err
	}
	cfg.IdleTimeout, err = parseDuration(raw.IdleTimeout, cfg.IdleTimeout)
	if err != nil {
		return Config{}, err
	}
	cfg.ReadTimeout, err = parseDuration(raw.ReadTimeout, cfg.ReadTimeout)
	if err != nil {
		return Config{}, err
	}
	cfg.WriteTimeout, err = parseDuration(raw.WriteTimeout, cfg.WriteTimeout)
	if err != nil {
		return Config{}, err
	}
	cfg.ShutdownTimeout, err = parseDuration(raw.ShutdownTimeout, cfg.ShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	if raw.LogLevel != "" {
		cfg.LogLevel = getLogLevel(raw.LogLevel)
	}
	return cfg, nil
}

func getLogLevel(v string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(v)); err != nil {
		return slog.LevelInfo
	}
	return level
}
