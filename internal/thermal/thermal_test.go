package thermal

import (
	"math"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

func constant(temp float64) Series {
	return Series{{Time: t0, Temp: temp}, {Time: t0.Add(48 * time.Hour), Temp: temp}}
}

func near(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

func TestSeriesAt(t *testing.T) {
	s := Series{
		{Time: t0, Temp: 10},
		{Time: t0.Add(time.Hour), Temp: 6},
		{Time: t0.Add(2 * time.Hour), Temp: 8},
	}
	tests := []struct {
		at   time.Duration
		want float64
	}{
		{-time.Hour, 10}, // before the start: held
		{0, 10},
		{15 * time.Minute, 9},
		{time.Hour, 6},
		{90 * time.Minute, 7},
		{2 * time.Hour, 8},
		{5 * time.Hour, 8}, // after the end: held
	}
	for _, tc := range tests {
		if got := s.At(t0.Add(tc.at)); !near(got, tc.want, 1e-9) {
			t.Errorf("At(+%s) = %v, want %v", tc.at, got, tc.want)
		}
	}
	if !math.IsNaN(Series(nil).At(t0)) {
		t.Error("At on an empty series should be NaN")
	}
}

func TestPredictMatchesExactSolution(t *testing.T) {
	m := Model{K: 0.1}

	got := m.Predict(t0, 20, constant(5), 12*time.Hour, time.Hour)

	if len(got) != 13 {
		t.Fatalf("got %d points, want 13 (start plus 12 hours)", len(got))
	}
	for h, p := range got {
		want := 5 + 15*math.Exp(-0.1*float64(h))
		if !near(p.Temp, want, 1e-9) {
			t.Errorf("hour %d: %.4f, want %.4f", h, p.Temp, want)
		}
		if !p.Time.Equal(t0.Add(time.Duration(h) * time.Hour)) {
			t.Errorf("hour %d at %s", h, p.Time)
		}
	}
}

func TestPredictFollowsChangingWeather(t *testing.T) {
	m := Model{K: 0.2}
	warming := Series{{Time: t0, Temp: 0}, {Time: t0.Add(6 * time.Hour), Temp: 30}}

	got := m.Predict(t0, 15, warming, 6*time.Hour, time.Hour)

	// It cools at first while it is colder outside, then warms once the
	// outdoor temperature passes the indoor one.
	if got[1].Temp >= 15 {
		t.Errorf("after 1h: %.2f, want below 15", got[1].Temp)
	}
	if got.Last().Temp <= got[3].Temp {
		t.Errorf("did not warm up later: %.2f at 3h, %.2f at 6h", got[3].Temp, got.Last().Temp)
	}
}

func TestPredictHandlesUnevenHorizon(t *testing.T) {
	got := Model{K: 0.05}.Predict(t0, 20, constant(10), 50*time.Minute, 15*time.Minute)

	if n := len(got); n != 5 {
		t.Fatalf("got %d points, want 5", n)
	}
	if !got.Last().Time.Equal(t0.Add(50 * time.Minute)) {
		t.Errorf("last point at %s, want +50m", got.Last().Time)
	}
}

// simulate produces samples of a house with cooling rate k, sampled every
// 15 minutes, with the heating on for the given hours of the day.
func simulate(k float64, hours int, outdoor float64, heatingHours map[int]bool) []Sample {
	var out []Sample
	temp := 20.0
	for i := 0; i < hours*4; i++ {
		ts := t0.Add(time.Duration(i) * 15 * time.Minute)
		heating := heatingHours[ts.Hour()]
		out = append(out, Sample{Time: ts, Indoor: temp, Outdoor: outdoor, Heating: heating})
		if heating {
			temp += 0.5
		} else {
			temp = outdoor + (temp-outdoor)*math.Exp(-k*0.25)
		}
	}
	return out
}

func TestFitRecoversCoolingRate(t *testing.T) {
	samples := simulate(0.08, 24, 2, map[int]bool{6: true, 7: true, 17: true, 18: true})

	m := Fit(samples)

	if !m.Fitted() {
		t.Fatalf("not fitted from %d samples", len(samples))
	}
	if !near(m.K, 0.08, 0.004) {
		t.Errorf("K = %.4f, want about 0.08", m.K)
	}
}

func TestFitSkipsHeatingAndSettling(t *testing.T) {
	samples := simulate(0.08, 12, 2, map[int]bool{3: true})
	// While heating, the house warms; that must not drag k negative.
	m := Fit(samples)
	if m.K < 0.07 {
		t.Errorf("K = %.4f; heating periods leaked into the fit", m.K)
	}

	// Pretend the radiators kept warming the room for a while after the
	// boiler stopped: that period must be ignored too.
	for i := range samples {
		if h := samples[i].Time.Sub(t0); h >= 4*time.Hour && h < 4*time.Hour+45*time.Minute {
			samples[i].Indoor += 1
		}
	}
	if m2 := Fit(samples); !near(m2.K, m.K, 1e-9) {
		t.Errorf("K changed from %.4f to %.4f after editing readings in the settle period", m.K, m2.K)
	}
}

func TestFitFallsBackToDefault(t *testing.T) {
	tests := map[string][]Sample{
		"no samples":        nil,
		"too few":           simulate(0.1, 2, 5, nil),
		"always heating":    simulate(0.1, 12, 5, map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true, 9: true, 10: true, 11: true}),
		"no indoor-out gap": simulate(0.1, 12, 20, nil),
	}
	for name, samples := range tests {
		t.Run(name, func(t *testing.T) {
			m := Fit(samples)
			if m.Fitted() || m.K != DefaultK {
				t.Errorf("Fit = %+v, want the default", m)
			}
		})
	}
}

func TestFitIgnoresGaps(t *testing.T) {
	var samples []Sample
	// Readings 3 hours apart: the agent was down in between.
	for i := 0; i < 30; i++ {
		samples = append(samples, Sample{Time: t0.Add(time.Duration(i) * 3 * time.Hour), Indoor: 20 - float64(i%2), Outdoor: 0})
	}
	if m := Fit(samples); m.Fitted() {
		t.Errorf("fitted %+v from pairs that are too far apart", m)
	}
}

func TestFitClampsK(t *testing.T) {
	var samples []Sample
	// Indoor rising while colder outside and the heating off (say, a sunny
	// day) would give a negative k.
	for i := 0; i < 20; i++ {
		samples = append(samples, Sample{Time: t0.Add(time.Duration(i) * 15 * time.Minute), Indoor: 18 + 0.2*float64(i), Outdoor: 5})
	}
	if m := Fit(samples); m.K != minK {
		t.Errorf("K = %v, want it clamped to %v", m.K, minK)
	}
}

func TestTimeConstant(t *testing.T) {
	if got := (Model{K: 0.05}).TimeConstant(); got != 20*time.Hour {
		t.Errorf("TimeConstant = %s, want 20h", got)
	}
}

func settings() Settings {
	return Settings{Target: 20, MaxBoost: 1.5, Lookahead: time.Hour, Horizon: 12 * time.Hour, Step: 0.5, Min: 5, Max: 32}
}

func TestDecideBoostsAheadOfColdWeather(t *testing.T) {
	m := Model{K: 0.05}

	d := Decide(m, t0, 19.6, constant(0), settings())

	// From 20°C with 0°C outside, an hour loses 20·(1-e^-0.05) ≈ 0.98°C.
	if !near(d.Loss, 0.975, 0.01) {
		t.Errorf("Loss = %.3f, want about 0.975", d.Loss)
	}
	if d.Setpoint != 21 {
		t.Errorf("Setpoint = %v, want 21", d.Setpoint)
	}
	if !strings.Contains(d.Reason, "would lose 1.0°C over the next 1h") {
		t.Errorf("Reason = %q", d.Reason)
	}
	if len(d.Passive) != 13 {
		t.Errorf("Passive has %d points, want 13", len(d.Passive))
	}
}

func TestDecideCapsBoost(t *testing.T) {
	s := settings()
	s.MaxBoost = 0.5

	d := Decide(Model{K: 0.2}, t0, 18, constant(-5), s)

	if d.Setpoint != 20.5 {
		t.Errorf("Setpoint = %v, want 20.5", d.Setpoint)
	}
}

func TestDecideCoastsWhenWarmEnough(t *testing.T) {
	d := Decide(Model{K: 0.05}, t0, 22, constant(0), settings())

	if d.Setpoint != 20 {
		t.Errorf("Setpoint = %v, want 20", d.Setpoint)
	}
	if !strings.Contains(d.Reason, "coast") {
		t.Errorf("Reason = %q", d.Reason)
	}
}

func TestDecideHoldsTargetInMildWeather(t *testing.T) {
	d := Decide(Model{K: 0.05}, t0, 19, constant(21), settings())

	if d.Setpoint != 20 || d.Loss > 0 {
		t.Errorf("Setpoint = %v, Loss = %v; want 20 and no loss", d.Setpoint, d.Loss)
	}
	if !strings.Contains(d.Reason, "mild") {
		t.Errorf("Reason = %q", d.Reason)
	}
}

func TestDecideRespectsThermostatLimits(t *testing.T) {
	s := settings()
	s.Target = 21.8
	s.Max = 22

	d := Decide(Model{K: 0.1}, t0, 21, constant(-10), s)

	if d.Setpoint != 22 {
		t.Errorf("Setpoint = %v, want it clamped to 22", d.Setpoint)
	}
}

func TestDecideWithBoostDisabled(t *testing.T) {
	s := settings()
	s.MaxBoost = 0

	if d := Decide(Model{K: 0.1}, t0, 18, constant(-10), s); d.Setpoint != 20 {
		t.Errorf("Setpoint = %v, want 20", d.Setpoint)
	}
}
