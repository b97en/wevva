package main

import (
	"log"
	"path/filepath"
	"time"
	"wevva/internal/app" // Adjust the import path based on your module name
)

func main() {
	// Determine the path to the database file
	dbPath := filepath.Join(".", "weather.db")

	// Initialize the weather service
	wevvaSvc, err := app.NewWevvaSvc(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize weather service: %v", err)
	}

	wevvaSvc.ViewWeatherData()

	// Start the ticker to periodically fetch and update weather data
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	// Run once immediately before entering the loop
	if err := wevvaSvc.UpdateWeatherData(); err != nil {
		log.Printf("Error updating weather data: %v", err)
	}

	for range ticker.C {
		if err := wevvaSvc.UpdateWeatherData(); err != nil {
			log.Printf("Error updating weather data: %v", err)
		}
	}
}
