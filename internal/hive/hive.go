// Package hive talks to a Hive thermostat through the Home Assistant REST API.
package hive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Climate is the state of a Home Assistant climate entity.
type Climate struct {
	EntityID string
	// Mode is the HVAC mode: "heat" (manual), "auto" (schedule) or "off".
	Mode string
	// Action is what the boiler is doing: "heating", "idle" or "off".
	Action  string
	Current float64
	Target  float64
	// Preset is "boost" while a Hive boost is running.
	Preset string
	Min    float64
	Max    float64
	Step   float64
}

// Heating reports whether the boiler is currently heating.
func (c Climate) Heating() bool {
	return c.Action == "heating"
}

// Location is the home's position as configured in Home Assistant.
type Location struct {
	Latitude  float64
	Longitude float64
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns a client for the Home Assistant instance at baseURL,
// authenticating with a long-lived access token.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

type stateResponse struct {
	EntityID   string `json:"entity_id"`
	State      string `json:"state"`
	Attributes struct {
		CurrentTemperature *float64 `json:"current_temperature"`
		Temperature        *float64 `json:"temperature"`
		HVACAction         string   `json:"hvac_action"`
		PresetMode         string   `json:"preset_mode"`
		MinTemp            float64  `json:"min_temp"`
		MaxTemp            float64  `json:"max_temp"`
		TargetTempStep     float64  `json:"target_temp_step"`
	} `json:"attributes"`
}

// Climate fetches the current state of a climate entity.
func (c *Client) Climate(ctx context.Context, entityID string) (Climate, error) {
	var s stateResponse
	if err := c.do(ctx, http.MethodGet, "/api/states/"+url.PathEscape(entityID), nil, &s); err != nil {
		return Climate{}, err
	}

	if s.State == "unavailable" || s.State == "unknown" {
		return Climate{}, fmt.Errorf("%s is %s in Home Assistant", entityID, s.State)
	}
	a := s.Attributes
	if a.CurrentTemperature == nil {
		return Climate{}, fmt.Errorf("%s has no current_temperature; is it a climate entity?", entityID)
	}

	cl := Climate{
		EntityID: s.EntityID,
		Mode:     s.State,
		Action:   a.HVACAction,
		Current:  *a.CurrentTemperature,
		Preset:   a.PresetMode,
		Min:      a.MinTemp,
		Max:      a.MaxTemp,
		Step:     a.TargetTempStep,
	}
	if a.Temperature != nil {
		cl.Target = *a.Temperature
	}
	// Hive reports these, but fall back to its documented limits if a
	// different integration leaves them out.
	if cl.Min == 0 && cl.Max == 0 {
		cl.Min, cl.Max = 5, 32
	}
	if cl.Step <= 0 {
		cl.Step = 0.5
	}
	return cl, nil
}

// SetTemperature changes a climate entity's target temperature.
func (c *Client) SetTemperature(ctx context.Context, entityID string, celsius float64) error {
	body := map[string]any{"entity_id": entityID, "temperature": celsius}
	return c.do(ctx, http.MethodPost, "/api/services/climate/set_temperature", body, nil)
}

// Location fetches the home's latitude and longitude from Home Assistant.
func (c *Client) Location(ctx context.Context) (Location, error) {
	var cfg struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/config", nil, &cfg); err != nil {
		return Location{}, err
	}
	if cfg.Latitude == nil || cfg.Longitude == nil {
		return Location{}, errors.New("home assistant has no home location configured")
	}
	return Location{Latitude: *cfg.Latitude, Longitude: *cfg.Longitude}, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("home assistant %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		hint := ""
		if resp.StatusCode == http.StatusUnauthorized {
			hint = " (check the access token)"
		}
		return fmt.Errorf("home assistant %s %s: %s%s: %s", method, path, resp.Status, hint, strings.TrimSpace(string(msg)))
	}

	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("home assistant %s %s: decoding response: %w", method, path, err)
	}
	return nil
}
