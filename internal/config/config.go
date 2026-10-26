// Package config reads wevva's settings from environment variables and
// command-line flags. Flags override the environment; secrets can only be
// set in the environment.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HAURL   string
	HAToken string
	Entity  string

	WeatherAPIKey string
	WeatherAPI    string
	FetcherPath   string

	// HasLocation is false when no latitude and longitude are set, in
	// which case Home Assistant's home location is used.
	HasLocation bool
	Latitude    float64
	Longitude   float64

	Target       float64
	MaxBoost     float64
	Lookahead    time.Duration
	Interval     time.Duration
	ControlModes []string

	DBPath  string
	DryRun  bool
	Once    bool
	Debug   bool
	History time.Duration
}

// Load builds the configuration from args (without the program name) and
// the environment. It returns flag.ErrHelp if -h was given.
func Load(args []string, getenv func(string) string, usage io.Writer) (Config, error) {
	env := envReader{getenv: getenv}
	var c Config
	var lat, lon, modes string

	fs := flag.NewFlagSet("wevva", flag.ContinueOnError)
	fs.SetOutput(usage)
	fs.Usage = func() {
		fmt.Fprint(usage, "Usage: wevva [flags]\n\n"+
			"wevva reads your Hive thermostat through Home Assistant, the outdoor 'feels like'\n"+
			"forecast from OpenWeather, and raises the setpoint ahead of the cold.\n\n"+
			"Secrets come from the environment only:\n"+
			"  WEVVA_HA_TOKEN     Home Assistant long-lived access token\n"+
			"  WEATHER_API_KEY    OpenWeather API key\n\n"+
			"Flags (each defaults to the environment variable shown):\n")
		fs.PrintDefaults()
	}

	fs.StringVar(&c.HAURL, "ha-url", env.str("WEVVA_HA_URL", ""), "Home Assistant base URL, e.g. http://homeassistant.local:8123 (WEVVA_HA_URL)")
	fs.StringVar(&c.Entity, "entity", env.str("WEVVA_CLIMATE_ENTITY", "climate.heating"), "Hive climate entity ID (WEVVA_CLIMATE_ENTITY)")
	fs.StringVar(&c.WeatherAPI, "weather-api", env.str("WEVVA_WEATHER_API", "forecast"), "OpenWeather API: forecast (free) or onecall (One Call 3.0) (WEVVA_WEATHER_API)")
	fs.StringVar(&c.FetcherPath, "fetcher", env.str("WEVVA_FETCHER", ""), "path to the retrieve-weather executable; found automatically if empty (WEVVA_FETCHER)")
	fs.StringVar(&lat, "lat", env.str("WEVVA_LATITUDE", ""), "home latitude; Home Assistant's if empty (WEVVA_LATITUDE)")
	fs.StringVar(&lon, "lon", env.str("WEVVA_LONGITUDE", ""), "home longitude, west is negative; Home Assistant's if empty (WEVVA_LONGITUDE)")
	fs.Float64Var(&c.Target, "target", env.float("WEVVA_TARGET_TEMP", 20), "temperature to hold, °C (WEVVA_TARGET_TEMP)")
	fs.Float64Var(&c.MaxBoost, "max-boost", env.float("WEVVA_MAX_BOOST", 1), "most to raise the setpoint above the target, °C; 0 disables (WEVVA_MAX_BOOST)")
	fs.DurationVar(&c.Lookahead, "lookahead", env.duration("WEVVA_LOOKAHEAD", time.Hour), "how far ahead to anticipate heat loss (WEVVA_LOOKAHEAD)")
	fs.DurationVar(&c.Interval, "interval", env.duration("WEVVA_INTERVAL", 15*time.Minute), "time between cycles (WEVVA_INTERVAL)")
	fs.StringVar(&modes, "control-modes", env.str("WEVVA_CONTROL_MODES", "heat"), "comma-separated HVAC modes wevva may adjust: heat, auto (WEVVA_CONTROL_MODES)")
	fs.StringVar(&c.DBPath, "db", env.str("WEVVA_DB", "wevva.db"), "bbolt database file (WEVVA_DB)")
	fs.BoolVar(&c.DryRun, "dry-run", env.bool("WEVVA_DRY_RUN", false), "decide but never change the thermostat (WEVVA_DRY_RUN)")
	fs.BoolVar(&c.Once, "once", false, "run one cycle and exit")
	fs.BoolVar(&c.Debug, "debug", env.bool("WEVVA_DEBUG", false), "log the full hourly prediction (WEVVA_DEBUG)")
	fs.DurationVar(&c.History, "history", 0, "print what was recorded over this long, e.g. 24h, and exit")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if err := errors.Join(env.errs...); err != nil {
		return Config{}, err
	}

	c.HAToken = getenv("WEVVA_HA_TOKEN")
	c.WeatherAPIKey = getenv("WEATHER_API_KEY")
	c.ControlModes = splitList(modes)

	if c.History > 0 {
		if c.DBPath == "" {
			return Config{}, errors.New("-db must not be empty")
		}
		return c, nil
	}

	var err error
	c.HasLocation, c.Latitude, c.Longitude, err = parseLocation(lat, lon)
	if err != nil {
		return Config{}, err
	}
	return c, c.validate()
}

func (c Config) validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}

	u, err := url.Parse(c.HAURL)
	check(c.HAURL != "", "WEVVA_HA_URL (or -ha-url) is required")
	check(c.HAURL == "" || (err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""),
		"Home Assistant URL %q must look like http://homeassistant.local:8123", c.HAURL)
	check(c.HAToken != "", "WEVVA_HA_TOKEN is required")
	check(c.WeatherAPIKey != "", "WEATHER_API_KEY is required")
	check(strings.HasPrefix(c.Entity, "climate."), "entity %q must be a climate entity, e.g. climate.heating", c.Entity)
	check(c.WeatherAPI == "forecast" || c.WeatherAPI == "onecall", "weather API %q must be forecast or onecall", c.WeatherAPI)
	check(c.Target >= 5 && c.Target <= 30, "target %.1f°C must be between 5 and 30", c.Target)
	check(c.MaxBoost >= 0 && c.MaxBoost <= 3, "max boost %.1f°C must be between 0 and 3", c.MaxBoost)
	check(c.Lookahead >= 15*time.Minute && c.Lookahead <= 12*time.Hour, "lookahead %s must be between 15m and 12h", c.Lookahead)
	check(c.Interval >= time.Minute, "interval %s must be at least 1m", c.Interval)
	check(c.DBPath != "", "-db must not be empty")
	check(len(c.ControlModes) > 0, "at least one control mode is needed")
	for _, m := range c.ControlModes {
		check(slices.Contains([]string{"heat", "auto"}, m), "control mode %q must be heat or auto", m)
	}
	return errors.Join(errs...)
}

func parseLocation(lat, lon string) (bool, float64, float64, error) {
	if lat == "" && lon == "" {
		return false, 0, 0, nil
	}
	if lat == "" || lon == "" {
		return false, 0, 0, errors.New("set both latitude and longitude, or neither to use Home Assistant's")
	}
	la, err := strconv.ParseFloat(lat, 64)
	if err != nil || la < -90 || la > 90 {
		return false, 0, 0, fmt.Errorf("latitude %q must be a number between -90 and 90", lat)
	}
	lo, err := strconv.ParseFloat(lon, 64)
	if err != nil || lo < -180 || lo > 180 {
		return false, 0, 0, fmt.Errorf("longitude %q must be a number between -180 and 180", lon)
	}
	return true, la, lo, nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// envReader reads typed defaults from the environment, collecting parse
// errors so they can all be reported at once.
type envReader struct {
	getenv func(string) string
	errs   []error
}

func (e *envReader) str(name, def string) string {
	if v := e.getenv(name); v != "" {
		return v
	}
	return def
}

func (e *envReader) float(name string, def float64) float64 {
	v := e.getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s=%q is not a number", name, v))
		return def
	}
	return f
}

func (e *envReader) duration(name string, def time.Duration) time.Duration {
	v := e.getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s=%q is not a duration like 15m or 1h", name, v))
		return def
	}
	return d
}

func (e *envReader) bool(name string, def bool) bool {
	v := e.getenv(name)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s=%q is not true or false", name, v))
		return def
	}
	return b
}
