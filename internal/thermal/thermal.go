// Package thermal models how a home's indoor temperature drifts towards the
// outdoor temperature while the heating is off, and uses that model to pick
// a thermostat setpoint ahead of the weather.
//
// The model is Newton's law of cooling:
//
//	dT/dt = -k · (T - T_outdoor)
//
// where k is the home's cooling rate per hour. k is learned from the
// agent's own history of indoor and outdoor temperatures.
package thermal

import (
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	// DefaultK is used until there is enough history to fit k. It is a
	// time constant of 20 hours, middling for a UK house.
	DefaultK = 0.05

	// MinFitPairs is how many usable observation pairs Fit needs before it
	// trusts its estimate over DefaultK.
	MinFitPairs = 12

	// k is clamped to this range so a noisy fit cannot produce a house
	// that never cools or one that cools in minutes.
	minK = 0.005
	maxK = 0.5

	// Radiators keep warming a room after the boiler stops, so readings
	// this soon after heating are not passive cooling.
	settleTime = time.Hour

	// Pairs further apart than this (agent down) or closer together than
	// minPairGap (too little change to measure) are skipped.
	minPairGap = 5 * time.Minute
	maxPairGap = 2 * time.Hour

	// Pairs with less indoor-outdoor difference than this carry more
	// sensor noise than signal.
	minDrive = 2.0

	integrationStep = 5 * time.Minute
)

// Point is a temperature at a moment in time.
type Point struct {
	Time time.Time
	Temp float64
}

// Series is a temperature time series, oldest point first.
type Series []Point

// At linearly interpolates the series at t, holding the first and last
// values outside its range. It returns NaN for an empty series.
func (s Series) At(t time.Time) float64 {
	if len(s) == 0 {
		return math.NaN()
	}
	i := sort.Search(len(s), func(i int) bool { return s[i].Time.After(t) })
	if i == 0 {
		return s[0].Temp
	}
	if i == len(s) {
		return s[len(s)-1].Temp
	}
	a, b := s[i-1], s[i]
	frac := float64(t.Sub(a.Time)) / float64(b.Time.Sub(a.Time))
	return a.Temp + (b.Temp-a.Temp)*frac
}

// Last returns the final point. It panics on an empty series.
func (s Series) Last() Point {
	return s[len(s)-1]
}

// Sample is one observation of the home.
type Sample struct {
	Time    time.Time
	Indoor  float64
	Outdoor float64
	Heating bool
}

// Model is a fitted cooling rate.
type Model struct {
	// K is the cooling rate per hour.
	K float64
	// Pairs is how many observation pairs the fit used; 0 means K is
	// DefaultK.
	Pairs int
}

// Fitted reports whether K was learned from history rather than defaulted.
func (m Model) Fitted() bool {
	return m.Pairs > 0
}

// TimeConstant is how long the home takes to close about 63% of the gap
// between indoor and outdoor temperature with the heating off.
func (m Model) TimeConstant() time.Duration {
	return time.Duration(float64(time.Hour) / m.K)
}

// Fit estimates the cooling rate from consecutive samples taken while the
// heating was off and had been off long enough for the radiators to cool.
//
// For each usable pair it takes the rate of change of the indoor
// temperature, y, and the indoor-outdoor difference, x, and fits y = -k·x
// by least squares through the origin.
func Fit(samples []Sample) Model {
	sorted := append([]Sample(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })

	var sxx, sxy float64
	var pairs int
	var lastHeating time.Time
	for i, b := range sorted {
		if b.Heating {
			lastHeating = b.Time
		}
		if i == 0 {
			continue
		}
		a := sorted[i-1]
		gap := b.Time.Sub(a.Time)
		switch {
		case a.Heating || b.Heating:
			continue
		case !lastHeating.IsZero() && a.Time.Sub(lastHeating) < settleTime:
			continue
		case gap < minPairGap || gap > maxPairGap:
			continue
		}

		x := (a.Indoor+b.Indoor)/2 - (a.Outdoor+b.Outdoor)/2
		if math.Abs(x) < minDrive {
			continue
		}
		y := (b.Indoor - a.Indoor) / gap.Hours()
		sxx += x * x
		sxy += x * y
		pairs++
	}

	if pairs < MinFitPairs || sxx == 0 {
		return Model{K: DefaultK}
	}
	k := math.Min(math.Max(-sxy/sxx, minK), maxK)
	return Model{K: k, Pairs: pairs}
}

// Predict simulates the indoor temperature with the heating off, starting
// at indoor at time start. It returns the starting point followed by one
// point every step until horizon has passed.
func (m Model) Predict(start time.Time, indoor float64, outdoor Series, horizon, step time.Duration) Series {
	out := Series{{Time: start, Temp: indoor}}
	t, temp := start, indoor
	for elapsed := time.Duration(0); elapsed < horizon; {
		next := min(step, horizon-elapsed)
		for done := time.Duration(0); done < next; {
			dt := min(integrationStep, next-done)
			// Holding the outdoor temperature at its midpoint value makes
			// each sub-step an exact solution of the cooling equation.
			o := outdoor.At(t.Add(dt / 2))
			temp = o + (temp-o)*math.Exp(-m.K*dt.Hours())
			t = t.Add(dt)
			done += dt
		}
		elapsed += next
		out = append(out, Point{Time: t, Temp: temp})
	}
	return out
}

// Settings tune the controller.
type Settings struct {
	// Target is the temperature to hold.
	Target float64
	// MaxBoost is the most the setpoint may be raised above Target.
	MaxBoost float64
	// Lookahead is how far ahead to anticipate heat loss.
	Lookahead time.Duration
	// Horizon is how far ahead to predict, for reporting.
	Horizon time.Duration
	// Step, Min and Max are the thermostat's setpoint resolution and limits.
	Step float64
	Min  float64
	Max  float64
}

// Decision is the controller's output for one run.
type Decision struct {
	Setpoint float64
	// Loss is how far the home would fall from Target over the lookahead
	// with the heating off; negative if it would warm up.
	Loss float64
	// Passive is the predicted indoor temperature with the heating off,
	// hourly over the horizon.
	Passive Series
	Reason  string
}

// Decide picks a setpoint that anticipates the coming heat loss.
//
// A thermostat only reacts once the room has already cooled below its
// setpoint, and colder weather both cools the room faster and makes it feel
// colder at the same air temperature. So when the forecast says the home
// would lose heat over the lookahead, the setpoint is raised by that loss,
// up to MaxBoost. If the home is warm enough to coast through the lookahead
// above Target, or the weather is warm enough that it would not lose heat
// at all, the setpoint is just Target.
func Decide(m Model, now time.Time, indoor float64, outdoor Series, s Settings) Decision {
	passive := m.Predict(now, indoor, outdoor, s.Horizon, time.Hour)
	loss := s.Target - m.Predict(now, s.Target, outdoor, s.Lookahead, s.Lookahead).Last().Temp

	lowest := math.Inf(1)
	for _, p := range m.Predict(now, indoor, outdoor, s.Lookahead, 15*time.Minute) {
		lowest = math.Min(lowest, p.Temp)
	}

	var boost float64
	var reason string
	switch {
	case loss <= 0:
		reason = fmt.Sprintf("the forecast is mild enough that the home would not lose heat over the next %s", human(s.Lookahead))
	case lowest >= s.Target:
		reason = fmt.Sprintf("the home can coast above %.1f°C for the next %s (lowest predicted %.1f°C)", s.Target, human(s.Lookahead), lowest)
	default:
		boost = math.Min(loss, s.MaxBoost)
		reason = fmt.Sprintf("with the heating off the home would lose %.1f°C over the next %s", loss, human(s.Lookahead))
	}

	return Decision{
		Setpoint: clampToThermostat(s.Target+boost, s),
		Loss:     loss,
		Passive:  passive,
		Reason:   reason,
	}
}

func clampToThermostat(v float64, s Settings) float64 {
	if s.Step > 0 {
		v = math.Round(v/s.Step) * s.Step
	}
	if s.Max > s.Min {
		v = math.Min(math.Max(v, s.Min), s.Max)
	}
	return v
}

func human(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}
