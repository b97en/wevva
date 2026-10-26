// Command wevva keeps a Hive thermostat ahead of the weather. Every few
// minutes it fetches the outdoor "feels like" forecast, reads the indoor
// temperature through Home Assistant, predicts how the home will cool and
// raises the setpoint ahead of the cold.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/b97en/wevva/internal/app"
	"github.com/b97en/wevva/internal/config"
	"github.com/b97en/wevva/internal/hive"
	"github.com/b97en/wevva/internal/weather"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wevva:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}

	if cfg.History > 0 {
		now := time.Now()
		return app.PrintHistory(os.Stdout, cfg.DBPath, now.Add(-cfg.History), now)
	}

	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ha := hive.NewClient(cfg.HAURL, cfg.HAToken)
	if !cfg.HasLocation {
		loc, err := ha.Location(ctx)
		if err != nil {
			return fmt.Errorf("no location set and could not read it from Home Assistant: %w", err)
		}
		cfg.Latitude, cfg.Longitude = loc.Latitude, loc.Longitude
		log.Info("using Home Assistant's home location", "lat", cfg.Latitude, "lon", cfg.Longitude)
	}

	fetcherPath := cfg.FetcherPath
	if fetcherPath == "" {
		if fetcherPath, err = weather.FindFetcher(); err != nil {
			return err
		}
	}

	fetcher := &weather.Fetcher{
		Path:      fetcherPath,
		APIKey:    cfg.WeatherAPIKey,
		API:       cfg.WeatherAPI,
		Latitude:  cfg.Latitude,
		Longitude: cfg.Longitude,
		Hours:     int(app.Horizon / time.Hour),
		Timeout:   time.Minute,
	}
	svc := app.New(app.Options{
		DBPath:       cfg.DBPath,
		EntityID:     cfg.Entity,
		Target:       cfg.Target,
		MaxBoost:     cfg.MaxBoost,
		Lookahead:    cfg.Lookahead,
		ControlModes: cfg.ControlModes,
		DryRun:       cfg.DryRun,
	}, fetcher, ha, log)

	log.Info("starting",
		"entity", cfg.Entity,
		"target", cfg.Target,
		"max_boost", cfg.MaxBoost,
		"interval", cfg.Interval.String(),
		"dry_run", cfg.DryRun,
		"fetcher", fetcherPath,
	)
	if cfg.Once {
		return svc.Tick(ctx)
	}
	return svc.Run(ctx, cfg.Interval)
}
