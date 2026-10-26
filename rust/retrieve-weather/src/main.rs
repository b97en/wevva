//! retrieve-weather prints the next few hours of OpenWeather "feels like"
//! temperatures as JSON on stdout, for the wevva Go agent to consume.
//!
//! Errors go to stderr with a non-zero exit code, so the agent can tell a
//! failed fetch apart from an empty forecast.

mod forecast;
mod openweather;

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{ensure, Context, Result};
use clap::Parser;

use crate::forecast::Report;
use crate::openweather::Api;

#[derive(Parser, Debug)]
#[command(version, about)]
struct Args {
    /// Latitude of the home in decimal degrees.
    #[arg(long, allow_negative_numbers = true)]
    lat: f64,

    /// Longitude of the home in decimal degrees (west of Greenwich is negative).
    #[arg(long, allow_negative_numbers = true)]
    lon: f64,

    /// How many hours to report, starting from the current hour.
    #[arg(long, default_value_t = 12, value_parser = clap::value_parser!(u32).range(1..=48))]
    hours: u32,

    /// Which OpenWeather API to call.
    #[arg(long, value_enum, default_value_t = Api::Forecast)]
    api: Api,

    /// OpenWeather API key.
    #[arg(long, env = "WEATHER_API_KEY", hide_env_values = true)]
    api_key: String,

    /// OpenWeather base URL (only useful for testing).
    #[arg(long, default_value = openweather::BASE_URL, hide = true)]
    base_url: String,
}

#[tokio::main(flavor = "current_thread")]
async fn main() {
    if let Err(err) = run(Args::parse()).await {
        eprintln!("retrieve-weather: {err:#}");
        std::process::exit(1);
    }
}

async fn run(args: Args) -> Result<()> {
    ensure!((-90.0..=90.0).contains(&args.lat), "--lat must be between -90 and 90");
    ensure!(
        (-180.0..=180.0).contains(&args.lon),
        "--lon must be between -180 and 180"
    );
    ensure!(!args.api_key.trim().is_empty(), "WEATHER_API_KEY is empty");

    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(20))
        .user_agent(concat!("wevva-retrieve-weather/", env!("CARGO_PKG_VERSION")))
        .build()
        .context("building HTTP client")?;

    let samples = openweather::fetch(&client, &args.base_url, args.api, args.lat, args.lon, &args.api_key).await?;

    let now = SystemTime::now().duration_since(UNIX_EPOCH)?.as_secs() as i64;
    let report = Report {
        provider: args.api.provider_name(),
        latitude: args.lat,
        longitude: args.lon,
        generated_at: now,
        hours: forecast::hourly(&samples, now, args.hours)?,
    };

    println!("{}", serde_json::to_string(&report)?);
    Ok(())
}
