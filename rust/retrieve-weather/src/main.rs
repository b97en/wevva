use reqwest;
use serde::{Serialize, Deserialize};
use std::env;
use std::time::{SystemTime, UNIX_EPOCH, Duration};

#[derive(Deserialize, Debug)]
struct WeatherResponse {
    hourly: Vec<HourlyWeather>,
}

#[derive(Deserialize, Debug)]
struct HourlyWeather {
    dt: i64,
    feels_like: f64, // 'Feels like' temperature in Kelvin
}

#[derive(Serialize, Debug, Clone)]
struct DailyTemperatureReport {
    day: String,
    temperatures: Vec<TemperatureRecord>,
}

#[derive(Serialize, Debug, Clone)]
struct TemperatureRecord {
    timestamp: i64,
    feels_like_celsius: f64,
}

#[tokio::main]
async fn main() {
    let api_key = env::var("WEATHER_API_KEY").expect("WEATHER_API_KEY not set");
    let lat = "51.5074";
    let lon = "0.1278";

    let now = SystemTime::now();
    let now_timestamp = now.duration_since(UNIX_EPOCH).unwrap().as_secs() as i64;
    let yesterday_timestamp = now - Duration::from_secs(86400); // 24 hours earlier
    let yesterday_timestamp_unix = yesterday_timestamp.duration_since(UNIX_EPOCH).unwrap().as_secs() as i64;

    // Fetch today's temperatures forecast for the next 12 hours
    let today_temperatures = fetch_day_temperatures(&api_key, lat, lon, None).await;

    // Fetch yesterday's temperatures for the same 12-hour window
    let yesterday_temperatures = fetch_day_temperatures(&api_key, lat, lon, Some(yesterday_timestamp_unix)).await;

    // Combine reports
    let reports = vec![
        DailyTemperatureReport { day: "today".to_string(), temperatures: today_temperatures },
        DailyTemperatureReport { day: "yesterday".to_string(), temperatures: yesterday_temperatures },
    ];

    // Serialize and print
    if let Ok(json) = serde_json::to_string(&reports) {
        println!("{}", json);
    }
}

async fn fetch_day_temperatures(api_key: &str, lat: &str, lon: &str, timestamp: Option<i64>) -> Vec<TemperatureRecord> {
    let url = match timestamp {
        Some(ts) => format!(
            "https://api.openweathermap.org/data/2.5/onecall/timemachine?lat={}&lon={}&dt={}&appid={}",
            lat, lon, ts, api_key
        ),
        None => format!(
            "https://api.openweathermap.org/data/2.5/onecall?lat={}&lon={}&exclude=minutely,daily,alerts&appid={}",
            lat, lon, api_key
        ),
    };

    match fetch_weather_data(&url).await {
        Ok(data) => data.hourly.into_iter()
            .map(|h| TemperatureRecord {
                timestamp: h.dt,
                feels_like_celsius: h.feels_like - 273.15,
            })
            .collect(),
        Err(_) => Vec::new(),
    }
}

async fn fetch_weather_data(url: &str) -> Result<WeatherResponse, reqwest::Error> {
    reqwest::get(url).await?.json::<WeatherResponse>().await
}
