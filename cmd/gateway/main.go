package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"ollama-gateway/internal/config"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	var opts config.LoaderOptions
	fs := flag.CommandLine
	config.RegisterFlags(fs, &opts)
	flag.Parse()

	cfg, err := config.LoadWithFlags(opts, "configs/config.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	logger.Info("configuration loaded", "listen_addr", cfg.Server.ListenAddr, "backends", len(cfg.Backends))

	// TODO: wire up auth, ratelimit, models/backends, proxy, usage, dashboard.
	_ = cfg // placeholder until subsequent phases
}
