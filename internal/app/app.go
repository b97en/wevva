package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"wevva/internal/db"
)

type DailyTemperature struct {
	Day          string              `json:"day"`
	Temperatures []TemperatureRecord `json:"temperatures"`
}

type TemperatureRecord struct {
	Timestamp        int64   `json:"timestamp"`
	FeelsLikeCelsius float64 `json:"feels_like_celsius"`
}

type WevvaSvc struct {
	db *db.DB
}

func NewWevvaSvc(dbPath string) (*WevvaSvc, error) {
	database, err := db.NewDB(dbPath)
	if err != nil {
		return nil, err
	}
	return &WevvaSvc{db: database}, nil
}

func (ws *WevvaSvc) UpdateWeatherData() error {
	output, err := ws.runRustExecutable()
	if err != nil {
		return err
	}

	var dailyReports []DailyTemperature
	if err := json.Unmarshal(output, &dailyReports); err != nil {
		return fmt.Errorf("error parsing JSON data: %v", err)
	}

	// Initialize slices to store categorized temperature data
	var otempsToday, otempsYday []float64
	var timestampsToday, timestampsYday []int64

	// Process and categorize the temperatures
	for _, daily := range dailyReports {
		for _, temp := range daily.Temperatures {
			if daily.Day == "today" {
				otempsToday = append(otempsToday, temp.FeelsLikeCelsius)
				timestampsToday = append(timestampsToday, temp.Timestamp)
			} else if daily.Day == "yesterday" {
				otempsYday = append(otempsYday, temp.FeelsLikeCelsius)
				timestampsYday = append(timestampsYday, temp.Timestamp)
			}
		}
	}

	// Convert int64 timestamps to time.Time
	timestampsTodayTime := convertTimestamps(timestampsToday)
	timestampsYdayTime := convertTimestamps(timestampsYday)

	// Assuming itempsToday and itempsYday are not currently used and set to nil or appropriate defaults
	itempsToday, itempsYday := []float64{}, []float64{} // Placeholder for indoor temperatures

	// Update the database with categorized data
	if err := ws.db.UpdateTemperatureData(otempsToday, otempsYday, itempsToday, itempsYday, append(timestampsTodayTime, timestampsYdayTime...)); err != nil {
		return fmt.Errorf("error updating temperature data: %v", err)
	}

	return nil
}

// runRustExecutable runs the Rust executable and returns its output.
func (ws *WevvaSvc) runRustExecutable() ([]byte, error) {
	pathToExecutable := "../../rust/retrieve-weather/target/release/wevva"
	cmd := exec.Command(pathToExecutable)
	cmd.Env = append(os.Environ(), "WEATHER_API_KEY="+os.Getenv("WEATHER_API_KEY"))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("error executing Rust binary: %v, stderr: %s", err, stderr.String())
	}

	return stdout.Bytes(), nil
}

func (ws *WevvaSvc) ViewWeatherData() {
	bucketNames := []string{"otemp_today", "otemp_yday", "itemp_today", "itemp_yday"}
	if err := ws.db.ViewBucketValues(bucketNames); err != nil {
		log.Printf("Failed to view weather data: %v", err)
	}
}

func convertTimestamps(timestamps []int64) []time.Time {
	var converted []time.Time
	for _, ts := range timestamps {
		converted = append(converted, time.Unix(ts, 0))
	}
	return converted
}
