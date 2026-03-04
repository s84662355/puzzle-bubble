package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	"game-gateway/internal/jsoncfg"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	configPath := flag.String("config", "configs/dbinit.json", "dbinit config file path")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
		os.Exit(1)
	}
	if cfg.DSN == "" {
		fmt.Fprintln(os.Stderr, "missing dsn in config")
		os.Exit(1)
	}

	data, err := os.ReadFile(cfg.File)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read sql file failed: %v\n", err)
		os.Exit(1)
	}

	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open mysql failed: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "ping mysql failed: %v\n", err)
		os.Exit(1)
	}

	statements := splitSQLStatements(string(data))
	for i, stmt := range statements {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			fmt.Fprintf(os.Stderr, "exec statement #%d failed: %v\nstatement: %s\n", i+1, err, stmt)
			os.Exit(1)
		}
	}

	fmt.Printf("schema applied successfully, statements=%d\n", len(statements))
}

type config struct {
	DSN  string `json:"dsn"`
	File string `json:"file"`
}

func loadConfig(path string) (config, error) {
	cfg := config{
		File: "db/schema_core.sql",
	}
	if err := jsoncfg.Load(path, &cfg); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func splitSQLStatements(sqlText string) []string {
	parts := strings.Split(sqlText, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		stmt := strings.TrimSpace(p)
		if stmt == "" {
			continue
		}
		out = append(out, stmt)
	}
	return out
}
