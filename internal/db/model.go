package db

type WeatherData struct {
	Timestamp   int64   `json:"timestamp"`
	Temperature float64 `json:"temperature"`
	FeelsLike   float64 `json:"feels_like"`
}
