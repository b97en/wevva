package config

import (
	"errors"
	"flag"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func minimal() map[string]string {
	return map[string]string{
		"WEVVA_HA_URL":    "http://homeassistant.local:8123",
		"WEVVA_HA_TOKEN":  "token",
		"WEATHER_API_KEY": "key",
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(nil, env(minimal()), io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if c.Entity != "climate.heating" || c.WeatherAPI != "forecast" || c.DBPath != "wevva.db" {
		t.Errorf("entity/api/db = %q/%q/%q", c.Entity, c.WeatherAPI, c.DBPath)
	}
	if c.Target != 20 || c.MaxBoost != 1 || c.Lookahead != time.Hour || c.Interval != 15*time.Minute {
		t.Errorf("target/boost/lookahead/interval = %v/%v/%v/%v", c.Target, c.MaxBoost, c.Lookahead, c.Interval)
	}
	if !slices.Equal(c.ControlModes, []string{"heat"}) {
		t.Errorf("control modes = %v", c.ControlModes)
	}
	if c.HasLocation || c.DryRun || c.Once {
		t.Errorf("location/dry-run/once = %v/%v/%v, want all false", c.HasLocation, c.DryRun, c.Once)
	}
	if c.HAToken != "token" || c.WeatherAPIKey != "key" {
		t.Error("secrets not read from the environment")
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	vars := minimal()
	vars["WEVVA_LATITUDE"] = "51.5074"
	vars["WEVVA_LONGITUDE"] = "-0.1278"
	vars["WEVVA_TARGET_TEMP"] = "19.5"
	vars["WEVVA_MAX_BOOST"] = "0.5"
	vars["WEVVA_LOOKAHEAD"] = "90m"
	vars["WEVVA_CONTROL_MODES"] = "heat, auto"
	vars["WEVVA_DRY_RUN"] = "true"

	c, err := Load(nil, env(vars), io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !c.HasLocation || c.Latitude != 51.5074 || c.Longitude != -0.1278 {
		t.Errorf("location = %v %v,%v", c.HasLocation, c.Latitude, c.Longitude)
	}
	if c.Target != 19.5 || c.MaxBoost != 0.5 || c.Lookahead != 90*time.Minute || !c.DryRun {
		t.Errorf("target/boost/lookahead/dry-run = %v/%v/%v/%v", c.Target, c.MaxBoost, c.Lookahead, c.DryRun)
	}
	if !slices.Equal(c.ControlModes, []string{"heat", "auto"}) {
		t.Errorf("control modes = %v", c.ControlModes)
	}
}

func TestFlagsOverrideEnvironment(t *testing.T) {
	vars := minimal()
	vars["WEVVA_TARGET_TEMP"] = "19"

	c, err := Load([]string{"-target", "21", "-once", "-lat=-33.9", "-lon=18.4"}, env(vars), io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Target != 21 || !c.Once || c.Latitude != -33.9 || c.Longitude != 18.4 {
		t.Errorf("target/once/lat/lon = %v/%v/%v/%v", c.Target, c.Once, c.Latitude, c.Longitude)
	}
}

func TestHistoryNeedsOnlyTheDatabase(t *testing.T) {
	c, err := Load([]string{"-history", "24h", "-db", "/tmp/x.db"}, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.History != 24*time.Hour || c.DBPath != "/tmp/x.db" {
		t.Errorf("history/db = %v/%q", c.History, c.DBPath)
	}
}

func TestHelp(t *testing.T) {
	var out strings.Builder
	_, err := Load([]string{"-h"}, env(nil), &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err = %v, want flag.ErrHelp", err)
	}
	if !strings.Contains(out.String(), "WEVVA_HA_TOKEN") || !strings.Contains(out.String(), "-max-boost") {
		t.Errorf("usage is missing details:\n%s", out.String())
	}
}

func TestLoadRejectsBadSettings(t *testing.T) {
	tests := map[string]struct {
		set     map[string]string
		args    []string
		wantErr string
	}{
		"missing url":      {set: map[string]string{"WEVVA_HA_URL": ""}, wantErr: "WEVVA_HA_URL"},
		"bad url":          {set: map[string]string{"WEVVA_HA_URL": "homeassistant.local"}, wantErr: "must look like"},
		"missing token":    {set: map[string]string{"WEVVA_HA_TOKEN": ""}, wantErr: "WEVVA_HA_TOKEN"},
		"missing key":      {set: map[string]string{"WEATHER_API_KEY": ""}, wantErr: "WEATHER_API_KEY"},
		"not climate":      {set: map[string]string{"WEVVA_CLIMATE_ENTITY": "sensor.hall"}, wantErr: "climate entity"},
		"bad api":          {set: map[string]string{"WEVVA_WEATHER_API": "metoffice"}, wantErr: "forecast or onecall"},
		"target too high":  {args: []string{"-target", "35"}, wantErr: "between 5 and 30"},
		"boost too big":    {args: []string{"-max-boost", "5"}, wantErr: "between 0 and 3"},
		"short lookahead":  {args: []string{"-lookahead", "5m"}, wantErr: "lookahead"},
		"short interval":   {args: []string{"-interval", "10s"}, wantErr: "at least 1m"},
		"off mode":         {args: []string{"-control-modes", "heat,off"}, wantErr: `"off" must be heat or auto`},
		"half location":    {set: map[string]string{"WEVVA_LATITUDE": "51.5"}, wantErr: "both latitude and longitude"},
		"bad latitude":     {args: []string{"-lat", "95", "-lon", "0"}, wantErr: "latitude"},
		"env not a number": {set: map[string]string{"WEVVA_TARGET_TEMP": "warm"}, wantErr: `WEVVA_TARGET_TEMP="warm"`},
		"env bad duration": {set: map[string]string{"WEVVA_INTERVAL": "15"}, wantErr: "WEVVA_INTERVAL"},
		"env bad bool":     {set: map[string]string{"WEVVA_DRY_RUN": "maybe"}, wantErr: "WEVVA_DRY_RUN"},
		"stray argument":   {args: []string{"run"}, wantErr: `unexpected argument "run"`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			vars := minimal()
			for k, v := range tc.set {
				vars[k] = v
			}
			_, err := Load(tc.args, env(vars), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadReportsAllProblems(t *testing.T) {
	_, err := Load(nil, env(nil), io.Discard)
	if err == nil {
		t.Fatal("Load succeeded with no configuration")
	}
	for _, want := range []string{"WEVVA_HA_URL", "WEVVA_HA_TOKEN", "WEATHER_API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}
