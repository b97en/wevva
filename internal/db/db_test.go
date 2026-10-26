package db

import (
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 10, 6, 0, 0, 0, time.UTC)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wevva.db")

	d, err := Open(path, time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening an existing file must work too.
	d, err = Open(path, time.Second)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	d.Close()
}

func TestOpenReportsLockedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wevva.db")
	d, err := Open(path, time.Second)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if _, err := Open(path, 50*time.Millisecond); err == nil {
		t.Fatal("second Open succeeded while the file was locked")
	}
}

func TestForecastRoundTrip(t *testing.T) {
	d := openTestDB(t)

	var hours []HourlyTemp
	for i := 0; i < 12; i++ {
		hours = append(hours, HourlyTemp{Time: t0.Add(time.Duration(i) * time.Hour), FeelsLike: float64(i) - 2.5})
	}
	if err := d.PutForecast(hours); err != nil {
		t.Fatalf("PutForecast: %v", err)
	}

	got, err := d.Forecast(t0.Add(2*time.Hour), t0.Add(5*time.Hour))
	if err != nil {
		t.Fatalf("Forecast: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d hours, want 3: %v", len(got), got)
	}
	for i, h := range got {
		want := hours[i+2]
		if !h.Time.Equal(want.Time) || h.FeelsLike != want.FeelsLike {
			t.Errorf("hour %d = %+v, want %+v", i, h, want)
		}
	}
}

func TestPutForecastReplacesSameHour(t *testing.T) {
	d := openTestDB(t)

	if err := d.PutForecast([]HourlyTemp{{Time: t0, FeelsLike: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutForecast([]HourlyTemp{{Time: t0, FeelsLike: 4.5}}); err != nil {
		t.Fatal(err)
	}

	got, err := d.Forecast(t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FeelsLike != 4.5 {
		t.Fatalf("got %+v, want one hour at 4.5", got)
	}
}

func TestObservationRoundTrip(t *testing.T) {
	d := openTestDB(t)

	want := []Observation{
		{Time: t0, Indoor: 19.5, Outdoor: 4, Heating: true, Setpoint: 20, Mode: "heat"},
		{Time: t0.Add(15 * time.Minute), Indoor: 19.9, Outdoor: 4.2, Heating: false, Setpoint: 20, Mode: "heat"},
	}
	for _, o := range want {
		if err := d.PutObservation(o); err != nil {
			t.Fatalf("PutObservation: %v", err)
		}
	}

	got, err := d.Observations(t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d observations, want %d", len(got), len(want))
	}
	for i := range want {
		if !got[i].Time.Equal(want[i].Time) {
			t.Errorf("observation %d time = %s, want %s", i, got[i].Time, want[i].Time)
		}
		got[i].Time = want[i].Time
		if got[i] != want[i] {
			t.Errorf("observation %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPrune(t *testing.T) {
	d := openTestDB(t)

	for i := 0; i < 5; i++ {
		ts := t0.Add(time.Duration(i) * time.Hour)
		if err := d.PutForecast([]HourlyTemp{{Time: ts, FeelsLike: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := d.PutObservation(Observation{Time: ts, Indoor: 20}); err != nil {
			t.Fatal(err)
		}
	}

	if err := d.Prune(t0.Add(3 * time.Hour)); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	far := t0.Add(24 * time.Hour)
	fc, _ := d.Forecast(time.Unix(0, 0), far)
	obs, _ := d.Observations(time.Unix(0, 0), far)
	if len(fc) != 2 || len(obs) != 2 {
		t.Fatalf("after prune: %d forecast hours and %d observations, want 2 of each", len(fc), len(obs))
	}
	if !fc[0].Time.Equal(t0.Add(3*time.Hour)) || !obs[0].Time.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("oldest kept entries are %s and %s, want %s", fc[0].Time, obs[0].Time, t0.Add(3*time.Hour))
	}
}

func TestFloat64Bytes(t *testing.T) {
	for _, f := range []float64{0, -12.75, 21.5, 1e-9} {
		if got := bytesToFloat64(float64ToBytes(f)); got != f {
			t.Errorf("round trip of %v gave %v", f, got)
		}
	}
}
