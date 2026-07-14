package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	"emby-migrator/internal/config"
	"emby-migrator/internal/exporter"
	"emby-migrator/internal/job"
	"emby-migrator/internal/web"
)

func main() {
	cfg := config.FromEnv()
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "imports"), 0o755); err != nil {
		log.Fatalf("create import packages dir: %v", err)
	}
	if err := os.MkdirAll(cfg.ConfigDir, 0o755); err != nil {
		log.Fatalf("create config dir: %v", err)
	}

	jobs := job.NewManagerWithOptions(job.ManagerOptions{
		LogDir:                filepath.Join(cfg.DataDir, "logs"),
		MaxMemoryLogEntries:   cfg.MaxMemoryLogEntries,
		MaxCompletedJobs:      cfg.MaxCompletedJobs,
		CompletedJobRetention: cfg.CompletedJobRetention,
		ReleaseMemoryOnFinish: cfg.ReleaseMemoryOnFinish,
	})
	service := exporter.NewService(cfg.DataDir, cfg.ImportRoot)
	handler := web.NewServer(cfg, jobs, service)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.ListenAddr, err)
	}

	serverErrors := make(chan error, 1)
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			serverErrors <- err
		}
	}()
	fmt.Fprintf(os.Stdout, "Emby Migrator listening on %s\n", localHTTPURL(listener.Addr().String()))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	select {
	case <-stop:
	case err := <-serverErrors:
		log.Fatalf("server failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown failed: %v\n", err)
		os.Exit(1)
	}
}

func localHTTPURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
