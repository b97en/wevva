package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/b97en/wevva/internal/db"
	"github.com/b97en/wevva/internal/hive"
	"github.com/b97en/wevva/internal/weather"
)

var t0 = time.Date(2026, 1, 10, 7, 20, 0, 0, time.UTC)

type fakeWeather struct {
	forecast weather.Forecast
	err      error
	calls    int
}

func (f *fakeWeather) Fetch(context.Context) (weather.Forecast, error) {
	f.calls++
	return f.forecast, f.err
}

// coldForecast is 12 hours at 0°C from the hour containing t0.
func coldForecast() weather.Forecast {
	fc := weather.Forecast{Provider: "test"}
	for h := 0; h < 12; h++ {
		fc.Hours = append(fc.Hours, weather.Hour{Time: t0.Truncate(time.Hour).Add(time.Duration(h) * time.Hour), FeelsLike: 0})
	}
	return fc
}

type fakeThermostat struct {
	mu      sync.Mutex
	climate hive.Climate
	err     error
	sets    []float64
}

func (f *fakeThermostat) Climate(context.Context, string) (hive.Climate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.climate, f.err
}

func (f *fakeThermostat) SetTemperature(_ context.Context, _ string, celsius float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, celsius)
	f.climate.Target = celsius
	return nil
}

func (f *fakeThermostat) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sets)
}

func heatingAt(indoor, target float64) hive.Climate {
	return hive.Climate{
		EntityID: "climate.heating", Mode: "heat", Action: "idle",
		Current: indoor, Target: target, Preset: "none", Min: 5, Max: 32, Step: 0.5,
	}
}

func newService(t *testing.T, w WeatherSource, th Thermostat, opts Options) (*Service, *bytes.Buffer) {
	t.Helper()
	if opts.DBPath == "" {
		opts.DBPath = filepath.Join(t.TempDir(), "wevva.db")
	}
	if opts.EntityID == "" {
		opts.EntityID = "climate.heating"
	}
	if opts.Target == 0 {
		opts.Target = 20
	}
	if opts.Lookahead == 0 {
		opts.Lookahead = time.Hour
	}
	if opts.ControlModes == nil {
		opts.ControlModes = []string{"heat"}
	}
	var logs bytes.Buffer
	s := New(opts, w, th, slog.New(slog.NewTextHandler(&logs, nil)))
	s.now = func() time.Time { return t0 }
	return s, &logs
}

func TestTickRaisesSetpointAheadOfCold(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	s, logs := newService(t, &fakeWeather{forecast: coldForecast()}, th, Options{MaxBoost: 1.5})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// With the default model, an hour at 0°C outside costs about 1°C.
	if len(th.sets) != 1 || th.sets[0] != 21 {
		t.Fatalf("setpoints sent = %v, want [21]", th.sets)
	}
	if !strings.Contains(logs.String(), "changed 20.0°C to 21.0°C") {
		t.Errorf("log does not report the change:\n%s", logs)
	}
}

func TestTickRecordsObservationAndForecast(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	th.climate.Action = "heating"
	s, _ := newService(t, &fakeWeather{forecast: coldForecast()}, th, Options{})

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	store, err := db.Open(s.opts.DBPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	obs, _ := store.Observations(t0.Add(-time.Hour), t0.Add(time.Hour))
	if len(obs) != 1 {
		t.Fatalf("got %d observations, want 1", len(obs))
	}
	want := db.Observation{Time: t0, Indoor: 19.6, Outdoor: 0, Heating: true, Setpoint: 20, Mode: "heat"}
	got := obs[0]
	if !got.Time.Equal(want.Time) {
		t.Errorf("observation time = %s, want %s", got.Time, want.Time)
	}
	got.Time = want.Time
	if got != want {
		t.Errorf("observation = %+v, want %+v", got, want)
	}
	if fc, _ := store.Forecast(t0.Truncate(time.Hour), t0.Add(24*time.Hour)); len(fc) != 12 {
		t.Errorf("stored %d forecast hours, want 12", len(fc))
	}
}

func TestTickLeavesThermostatAlone(t *testing.T) {
	tests := map[string]struct {
		climate func(hive.Climate) hive.Climate
		opts    Options
		wantLog string
	}{
		"schedule mode": {
			climate: func(c hive.Climate) hive.Climate { c.Mode = "auto"; return c },
			wantLog: `left alone: thermostat is in \"auto\" mode`,
		},
		"off": {
			climate: func(c hive.Climate) hive.Climate { c.Mode = "off"; return c },
			wantLog: `left alone: thermostat is in \"off\" mode`,
		},
		"boosting": {
			climate: func(c hive.Climate) hive.Climate { c.Preset = "boost"; return c },
			wantLog: "left alone: a Hive boost is running",
		},
		"already set": {
			climate: func(c hive.Climate) hive.Climate { c.Target = 21; return c },
			wantLog: "result=unchanged",
		},
		"dry run": {
			climate: func(c hive.Climate) hive.Climate { return c },
			opts:    Options{DryRun: true},
			wantLog: "dry run: would change 20.0°C to 21.0°C",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			th := &fakeThermostat{climate: tc.climate(heatingAt(19.6, 20))}
			tc.opts.MaxBoost = 1.5
			s, logs := newService(t, &fakeWeather{forecast: coldForecast()}, th, tc.opts)

			if err := s.Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}
			if len(th.sets) != 0 {
				t.Errorf("setpoints sent = %v, want none", th.sets)
			}
			if !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("log does not contain %q:\n%s", tc.wantLog, logs)
			}
		})
	}
}

func TestTickFallsBackToStoredForecast(t *testing.T) {
	w := &fakeWeather{forecast: coldForecast()}
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	s, logs := newService(t, w, th, Options{MaxBoost: 1.5})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}

	// An hour later the API is down.
	w.err = errors.New("OpenWeather returned 503")
	s.now = func() time.Time { return t0.Add(time.Hour) }
	th.climate.Target = 20
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	if !strings.Contains(logs.String(), "using the stored forecast") {
		t.Errorf("log does not mention the fallback:\n%s", logs)
	}
	if len(th.sets) != 2 || th.sets[1] != 21 {
		t.Errorf("setpoints sent = %v, want the second to be 21", th.sets)
	}
}

func TestTickFailsWithoutAnyForecast(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	s, _ := newService(t, &fakeWeather{err: errors.New("no network")}, th, Options{})

	err := s.Tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no stored forecast") {
		t.Fatalf("err = %v, want a no stored forecast error", err)
	}
	if len(th.sets) != 0 {
		t.Errorf("setpoints sent = %v, want none", th.sets)
	}
}

func TestTickFailsWhenThermostatUnreachable(t *testing.T) {
	th := &fakeThermostat{err: errors.New("connection refused")}
	s, _ := newService(t, &fakeWeather{forecast: coldForecast()}, th, Options{})

	if err := s.Tick(context.Background()); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the thermostat error", err)
	}
}

func TestRestore(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	s, _ := newService(t, &fakeWeather{forecast: coldForecast()}, th, Options{MaxBoost: 1.5})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := s.Restore(context.Background()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if want := []float64{21, 20}; !equal(th.sets, want) {
		t.Errorf("setpoints sent = %v, want %v", th.sets, want)
	}

	// A second restore has nothing to do.
	if err := s.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(th.sets) != 2 {
		t.Errorf("setpoints sent = %v after a second Restore", th.sets)
	}
}

func TestRestoreRespectsManualChange(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	s, _ := newService(t, &fakeWeather{forecast: coldForecast()}, th, Options{MaxBoost: 1.5})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	th.climate.Target = 23 // someone turned it up by hand
	if err := s.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(th.sets) != 1 {
		t.Errorf("setpoints sent = %v; Restore overrode a manual change", th.sets)
	}
}

func TestRestoreWithoutChanges(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	s, _ := newService(t, &fakeWeather{forecast: coldForecast()}, th, Options{DryRun: true, MaxBoost: 1.5})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(th.sets) != 0 {
		t.Errorf("setpoints sent = %v, want none", th.sets)
	}
}

func TestRunStopsAndRestores(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	w := &fakeWeather{forecast: coldForecast()}
	s, _ := newService(t, w, th, Options{MaxBoost: 1.5})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- s.Run(ctx, time.Hour) }()

	// Wait for the first cycle, then stop.
	deadline := time.Now().Add(5 * time.Second)
	for th.setCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if want := []float64{21, 20}; !equal(th.sets, want) {
		t.Errorf("setpoints sent = %v, want %v", th.sets, want)
	}
}

func TestPrintHistory(t *testing.T) {
	th := &fakeThermostat{climate: heatingAt(19.6, 20)}
	s, _ := newService(t, &fakeWeather{forecast: coldForecast()}, th, Options{DryRun: true})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := PrintHistory(&out, s.opts.DBPath, t0.Add(-24*time.Hour), t0); err != nil {
		t.Fatalf("PrintHistory: %v", err)
	}

	text := out.String()
	for _, want := range []string{"Observations since", "19.6", "heat", "Outdoor forecast", "feels like"} {
		if !strings.Contains(text, want) {
			t.Errorf("history output lacks %q:\n%s", want, text)
		}
	}
	if n := strings.Count(text, "0.0\n"); n != 12 {
		t.Errorf("found %d forecast rows, want 12:\n%s", n, text)
	}
}

func TestPrintHistoryEmpty(t *testing.T) {
	var out bytes.Buffer
	if err := PrintHistory(&out, filepath.Join(t.TempDir(), "new.db"), t0.Add(-time.Hour), t0); err != nil {
		t.Fatalf("PrintHistory: %v", err)
	}
	if strings.Count(out.String(), "(none)") != 2 {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func equal(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
