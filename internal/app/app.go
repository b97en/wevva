// Package app runs wevva's control loop: fetch the outdoor forecast, read the
// thermostat, record both, predict the indoor temperature and adjust the
// setpoint.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/b97en/wevva/internal/db"
	"github.com/b97en/wevva/internal/hive"
	"github.com/b97en/wevva/internal/thermal"
	"github.com/b97en/wevva/internal/weather"
)

const (
	// Horizon is how far ahead wevva forecasts and predicts.
	Horizon = 12 * time.Hour
	// fitWindow is how much history the thermal model is fitted to.
	fitWindow = 7 * 24 * time.Hour
	// retention is how long observations and forecasts are kept.
	retention = 30 * 24 * time.Hour
	// dbTimeout is how long to wait for another wevva process (for example
	// `wevva -history`) to release the database.
	dbTimeout = 5 * time.Second
)

// WeatherSource supplies the outdoor forecast.
type WeatherSource interface {
	Fetch(ctx context.Context) (weather.Forecast, error)
}

// Thermostat reads and adjusts the heating.
type Thermostat interface {
	Climate(ctx context.Context, entityID string) (hive.Climate, error)
	SetTemperature(ctx context.Context, entityID string, celsius float64) error
}

type Options struct {
	DBPath    string
	EntityID  string
	Target    float64
	MaxBoost  float64
	Lookahead time.Duration
	// ControlModes are the HVAC modes in which wevva may change the
	// setpoint, normally just "heat".
	ControlModes []string
	DryRun       bool
}

type Service struct {
	opts       Options
	weather    WeatherSource
	thermostat Thermostat
	log        *slog.Logger
	now        func() time.Time

	// lastSet is the setpoint wevva last applied, or 0 if it has not
	// changed the thermostat. restoreTo is the target rounded to the
	// thermostat's step.
	lastSet   float64
	restoreTo float64
}

func New(opts Options, w WeatherSource, t Thermostat, log *slog.Logger) *Service {
	return &Service{opts: opts, weather: w, thermostat: t, log: log, now: time.Now}
}

// Run runs a cycle straight away and then every interval until ctx is
// cancelled. A failed cycle is logged and retried at the next interval.
// When it stops, it puts the setpoint back to the target if wevva left it
// raised.
func (s *Service) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("cycle failed", "err", err)
		}
		select {
		case <-ctx.Done():
			s.log.Info("stopping")
			restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return s.Restore(restoreCtx)
		case <-ticker.C:
		}
	}
}

// Tick runs one cycle.
func (s *Service) Tick(ctx context.Context) error {
	now := s.now()

	store, err := db.Open(s.opts.DBPath, dbTimeout)
	if err != nil {
		return err
	}
	defer store.Close()

	outdoor, err := s.forecast(ctx, store, now)
	if err != nil {
		return err
	}

	climate, err := s.thermostat.Climate(ctx, s.opts.EntityID)
	if err != nil {
		return fmt.Errorf("reading thermostat: %w", err)
	}

	obs := db.Observation{
		Time:     now,
		Indoor:   climate.Current,
		Outdoor:  outdoor.At(now),
		Heating:  climate.Heating(),
		Setpoint: climate.Target,
		Mode:     climate.Mode,
	}
	if err := store.PutObservation(obs); err != nil {
		return fmt.Errorf("saving observation: %w", err)
	}
	s.log.Info("observed",
		"indoor", obs.Indoor,
		"outdoor_feels_like", round1(obs.Outdoor),
		"setpoint", obs.Setpoint,
		"mode", obs.Mode,
		"action", climate.Action,
	)

	history, err := store.Observations(now.Add(-fitWindow), now.Add(time.Second))
	if err != nil {
		return fmt.Errorf("loading history: %w", err)
	}
	model := thermal.Fit(samples(history))

	d := thermal.Decide(model, now, climate.Current, outdoor, thermal.Settings{
		Target:    s.opts.Target,
		MaxBoost:  s.opts.MaxBoost,
		Lookahead: s.opts.Lookahead,
		Horizon:   Horizon,
		Step:      climate.Step,
		Min:       climate.Min,
		Max:       climate.Max,
	})
	s.restoreTo = math.Round(s.opts.Target/climate.Step) * climate.Step

	s.log.Info("predicted",
		"cooling_rate_per_hour", round3(model.K),
		"time_constant", model.TimeConstant().Round(time.Minute).String(),
		"fitted_pairs", model.Pairs,
		"passive_in_3h", round1(d.Passive.At(now.Add(3*time.Hour))),
		"passive_in_6h", round1(d.Passive.At(now.Add(6*time.Hour))),
		"passive_in_12h", round1(d.Passive.At(now.Add(12*time.Hour))),
	)
	s.log.Debug("passive prediction", "hourly", hourly(d.Passive))

	result, err := s.apply(ctx, climate, d)
	if err != nil {
		return fmt.Errorf("setting thermostat: %w", err)
	}
	s.log.Info("decided", "setpoint", d.Setpoint, "why", d.Reason, "result", result)

	if err := store.Prune(now.Add(-retention)); err != nil {
		s.log.Warn("pruning old data failed", "err", err)
	}
	return nil
}

// forecast fetches a fresh forecast and stores it. If the fetch fails it
// falls back to the stored forecast, as long as that still covers now.
func (s *Service) forecast(ctx context.Context, store *db.DB, now time.Time) (thermal.Series, error) {
	fc, fetchErr := s.weather.Fetch(ctx)
	if fetchErr == nil {
		hours := make([]db.HourlyTemp, len(fc.Hours))
		for i, h := range fc.Hours {
			hours[i] = db.HourlyTemp{Time: h.Time, FeelsLike: h.FeelsLike}
		}
		if err := store.PutForecast(hours); err != nil {
			return nil, fmt.Errorf("saving forecast: %w", err)
		}
	} else if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	stored, err := store.Forecast(now.Truncate(time.Hour), now.Add(Horizon+time.Hour))
	if err != nil {
		return nil, fmt.Errorf("loading forecast: %w", err)
	}
	if fetchErr != nil {
		if len(stored) == 0 {
			return nil, fmt.Errorf("fetching weather: %w (no stored forecast to fall back on)", fetchErr)
		}
		s.log.Warn("fetching weather failed; using the stored forecast",
			"err", fetchErr, "stored_until", stored[len(stored)-1].Time.Format(time.DateTime))
	}

	series := make(thermal.Series, len(stored))
	for i, h := range stored {
		series[i] = thermal.Point{Time: h.Time, Temp: h.FeelsLike}
	}
	return series, nil
}

// apply changes the setpoint if wevva is allowed to and it differs from
// what the thermostat has. It returns a description of what it did.
func (s *Service) apply(ctx context.Context, c hive.Climate, d thermal.Decision) (string, error) {
	switch {
	case !slices.Contains(s.opts.ControlModes, c.Mode):
		return fmt.Sprintf("left alone: thermostat is in %q mode", c.Mode), nil
	case c.Preset == "boost":
		return "left alone: a Hive boost is running", nil
	case math.Abs(c.Target-d.Setpoint) < c.Step/2:
		return "unchanged", nil
	case s.opts.DryRun:
		return fmt.Sprintf("dry run: would change %.1f°C to %.1f°C", c.Target, d.Setpoint), nil
	}

	if err := s.thermostat.SetTemperature(ctx, c.EntityID, d.Setpoint); err != nil {
		return "", err
	}
	s.lastSet = d.Setpoint
	return fmt.Sprintf("changed %.1f°C to %.1f°C", c.Target, d.Setpoint), nil
}

// Restore puts the setpoint back to the target if wevva last left it
// somewhere else, so stopping wevva doesn't leave the home running warm.
// It leaves the thermostat alone if someone has changed it since.
func (s *Service) Restore(ctx context.Context) error {
	if s.lastSet == 0 || s.lastSet == s.restoreTo {
		return nil
	}
	c, err := s.thermostat.Climate(ctx, s.opts.EntityID)
	if err != nil {
		return fmt.Errorf("restoring setpoint: %w", err)
	}
	if !slices.Contains(s.opts.ControlModes, c.Mode) || math.Abs(c.Target-s.lastSet) >= c.Step/2 {
		return nil
	}
	if err := s.thermostat.SetTemperature(ctx, c.EntityID, s.restoreTo); err != nil {
		return fmt.Errorf("restoring setpoint: %w", err)
	}
	s.log.Info("restored setpoint", "setpoint", s.restoreTo)
	s.lastSet = s.restoreTo
	return nil
}

func samples(obs []db.Observation) []thermal.Sample {
	out := make([]thermal.Sample, len(obs))
	for i, o := range obs {
		out[i] = thermal.Sample{Time: o.Time, Indoor: o.Indoor, Outdoor: o.Outdoor, Heating: o.Heating}
	}
	return out
}

func hourly(s thermal.Series) []float64 {
	out := make([]float64, len(s))
	for i, p := range s {
		out[i] = round1(p.Temp)
	}
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
