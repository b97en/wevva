// Package weather runs the retrieve-weather Rust executable and parses the
// hourly "feels like" forecast it prints.
package weather

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FetcherName is the file name of the Rust executable.
const FetcherName = "retrieve-weather"

// Hour is the forecast "feels like" temperature at the start of an hour.
type Hour struct {
	Time      time.Time
	FeelsLike float64
}

// Forecast is an hourly outdoor forecast, oldest hour first.
type Forecast struct {
	Provider    string
	GeneratedAt time.Time
	Hours       []Hour
}

// Fetcher runs the Rust executable for one location.
type Fetcher struct {
	Path      string
	APIKey    string
	API       string // "forecast" or "onecall"
	Latitude  float64
	Longitude float64
	Hours     int
	Timeout   time.Duration
}

type report struct {
	Provider    string `json:"provider"`
	GeneratedAt int64  `json:"generated_at"`
	Hours       []struct {
		Timestamp        int64    `json:"timestamp"`
		FeelsLikeCelsius *float64 `json:"feels_like_celsius"`
	} `json:"hours"`
}

// Fetch runs the executable and returns its forecast.
func (f *Fetcher) Fetch(ctx context.Context) (Forecast, error) {
	if f.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, f.Path,
		"--lat="+strconv.FormatFloat(f.Latitude, 'f', -1, 64),
		"--lon="+strconv.FormatFloat(f.Longitude, 'f', -1, 64),
		"--hours="+strconv.Itoa(f.Hours),
		"--api="+f.API,
	)
	// The key goes in the environment rather than the arguments so it does
	// not show up in process listings.
	cmd.Env = append(os.Environ(), "WEATHER_API_KEY="+f.APIKey)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Forecast{}, fmt.Errorf("%s: %w: %s", filepath.Base(f.Path), err, msg)
		}
		return Forecast{}, fmt.Errorf("%s: %w", filepath.Base(f.Path), err)
	}

	return parse(stdout.Bytes())
}

func parse(out []byte) (Forecast, error) {
	var r report
	if err := json.Unmarshal(out, &r); err != nil {
		return Forecast{}, fmt.Errorf("parsing forecast JSON: %w", err)
	}
	if len(r.Hours) == 0 {
		return Forecast{}, errors.New("forecast has no hours")
	}

	fc := Forecast{Provider: r.Provider, GeneratedAt: time.Unix(r.GeneratedAt, 0)}
	for i, h := range r.Hours {
		if h.FeelsLikeCelsius == nil {
			return Forecast{}, fmt.Errorf("forecast hour %d has no feels_like_celsius", i)
		}
		if i > 0 && h.Timestamp <= r.Hours[i-1].Timestamp {
			return Forecast{}, fmt.Errorf("forecast hours are not in time order at hour %d", i)
		}
		fc.Hours = append(fc.Hours, Hour{Time: time.Unix(h.Timestamp, 0), FeelsLike: *h.FeelsLikeCelsius})
	}
	return fc, nil
}

// FindFetcher locates the Rust executable when no path is configured. It
// looks next to the running program, then on PATH, then in the cargo
// release directory of a source checkout.
func FindFetcher() (string, error) {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), FetcherName)
		if isExecutable(p) {
			return p, nil
		}
	}
	if p, err := exec.LookPath(FetcherName); err == nil {
		return p, nil
	}
	p := filepath.Join("rust", FetcherName, "target", "release", FetcherName)
	if isExecutable(p) {
		return filepath.Abs(p)
	}
	return "", fmt.Errorf("cannot find the %s executable; build it with `make build` or set WEVVA_FETCHER", FetcherName)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
