package weather

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeFetcher writes a shell script that records its arguments and
// environment, then prints stdout and exits with code.
func fakeFetcher(t *testing.T, stdout, stderr string, code int) (path, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake fetcher is a shell script")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"echo \"$@\" > '" + argsFile + "'\n" +
		"echo \"$WEATHER_API_KEY\" >> '" + argsFile + "'\n" +
		"cat <<'EOF'\n" + stdout + "\nEOF\n" +
		"echo '" + stderr + "' >&2\n" +
		"exit " + strconv.Itoa(code) + "\n"
	path = filepath.Join(dir, FetcherName)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, argsFile
}

const goodReport = `{"provider":"openweather-forecast-2.5","latitude":51.5,"longitude":-0.12,"generated_at":1767250000,
"hours":[{"timestamp":1767247200,"feels_like_celsius":4.5},{"timestamp":1767250800,"feels_like_celsius":3.75}]}`

func TestFetch(t *testing.T) {
	path, argsFile := fakeFetcher(t, goodReport, "", 0)
	f := &Fetcher{Path: path, APIKey: "key-abc", API: "forecast", Latitude: 51.5, Longitude: -0.12, Hours: 12, Timeout: 5 * time.Second}

	fc, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if fc.Provider != "openweather-forecast-2.5" || !fc.GeneratedAt.Equal(time.Unix(1767250000, 0)) {
		t.Errorf("metadata = %q at %s", fc.Provider, fc.GeneratedAt)
	}
	want := []Hour{
		{Time: time.Unix(1767247200, 0), FeelsLike: 4.5},
		{Time: time.Unix(1767250800, 0), FeelsLike: 3.75},
	}
	if len(fc.Hours) != len(want) {
		t.Fatalf("got %d hours, want %d", len(fc.Hours), len(want))
	}
	for i := range want {
		if !fc.Hours[i].Time.Equal(want[i].Time) || fc.Hours[i].FeelsLike != want[i].FeelsLike {
			t.Errorf("hour %d = %+v, want %+v", i, fc.Hours[i], want[i])
		}
	}

	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if lines[0] != "--lat=51.5 --lon=-0.12 --hours=12 --api=forecast" {
		t.Errorf("args = %q", lines[0])
	}
	if lines[1] != "key-abc" {
		t.Errorf("WEATHER_API_KEY = %q", lines[1])
	}
}

func TestFetchReportsStderr(t *testing.T) {
	path, _ := fakeFetcher(t, "", "retrieve-weather: OpenWeather returned 401 Unauthorized: Invalid API key.", 1)
	f := &Fetcher{Path: path, API: "forecast", Hours: 12}

	_, err := f.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Invalid API key.") {
		t.Fatalf("err = %v, want it to include the fetcher's stderr", err)
	}
}

func TestFetchMissingExecutable(t *testing.T) {
	f := &Fetcher{Path: filepath.Join(t.TempDir(), "nope"), API: "forecast", Hours: 12}

	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch succeeded with a missing executable")
	}
}

func TestParseRejectsBadReports(t *testing.T) {
	tests := map[string]string{
		"not json":     `oops`,
		"no hours":     `{"provider":"x","hours":[]}`,
		"missing temp": `{"hours":[{"timestamp":1}]}`,
		"out of order": `{"hours":[{"timestamp":2,"feels_like_celsius":1},{"timestamp":1,"feels_like_celsius":1}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parse([]byte(body)); err == nil {
				t.Errorf("parse(%s) succeeded", body)
			}
		})
	}
}
