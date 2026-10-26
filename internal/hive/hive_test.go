package hive

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const heatingState = `{
	"entity_id": "climate.heating",
	"state": "heat",
	"attributes": {
		"hvac_modes": ["auto", "heat", "off"],
		"min_temp": 5,
		"max_temp": 32,
		"target_temp_step": 0.5,
		"preset_modes": ["none", "boost"],
		"current_temperature": 19.4,
		"temperature": 20,
		"hvac_action": "heating",
		"preset_mode": "none",
		"friendly_name": "Heating"
	}
}`

func newServer(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-123" {
			t.Errorf("Authorization header = %q", got)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL+"/", "token-123")
}

func TestClimate(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/states/climate.heating" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		io.WriteString(w, heatingState)
	})

	got, err := c.Climate(context.Background(), "climate.heating")
	if err != nil {
		t.Fatalf("Climate: %v", err)
	}

	want := Climate{
		EntityID: "climate.heating",
		Mode:     "heat",
		Action:   "heating",
		Current:  19.4,
		Target:   20,
		Preset:   "none",
		Min:      5,
		Max:      32,
		Step:     0.5,
	}
	if got != want {
		t.Errorf("Climate = %+v\nwant      %+v", got, want)
	}
	if !got.Heating() {
		t.Error("Heating() = false, want true")
	}
}

func TestClimateDefaultsMissingLimits(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"entity_id":"climate.x","state":"off","attributes":{"current_temperature":18.0,"hvac_action":"off"}}`)
	})

	got, err := c.Climate(context.Background(), "climate.x")
	if err != nil {
		t.Fatalf("Climate: %v", err)
	}
	if got.Min != 5 || got.Max != 32 || got.Step != 0.5 {
		t.Errorf("limits = %v..%v step %v, want 5..32 step 0.5", got.Min, got.Max, got.Step)
	}
	if got.Target != 0 {
		t.Errorf("Target = %v, want 0 when the entity has no target", got.Target)
	}
}

func TestClimateErrors(t *testing.T) {
	tests := map[string]struct {
		status  int
		body    string
		wantErr string
	}{
		"unavailable":     {200, `{"entity_id":"climate.heating","state":"unavailable","attributes":{}}`, "is unavailable"},
		"not a climate":   {200, `{"entity_id":"sensor.temp","state":"19","attributes":{}}`, "no current_temperature"},
		"bad token":       {401, `401: Unauthorized`, "check the access token"},
		"missing entity":  {404, `{"message": "Entity not found."}`, "404"},
		"malformed reply": {200, `not json`, "decoding response"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})

			_, err := c.Climate(context.Background(), "climate.heating")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestSetTemperature(t *testing.T) {
	var got map[string]any
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/services/climate/set_temperature" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding body: %v", err)
		}
		io.WriteString(w, `[]`)
	})

	if err := c.SetTemperature(context.Background(), "climate.heating", 20.5); err != nil {
		t.Fatalf("SetTemperature: %v", err)
	}
	if got["entity_id"] != "climate.heating" || got["temperature"] != 20.5 {
		t.Errorf("request body = %v", got)
	}
}

func TestLocation(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		io.WriteString(w, `{"latitude": 51.5074, "longitude": -0.1278, "location_name": "Home"}`)
	})

	got, err := c.Location(context.Background())
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	if got != (Location{Latitude: 51.5074, Longitude: -0.1278}) {
		t.Errorf("Location = %+v", got)
	}
}
