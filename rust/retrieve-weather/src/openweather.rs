//! Calls the OpenWeather APIs and turns their responses into plain samples.

use anyhow::{bail, ensure, Context, Result};
use serde::Deserialize;

pub const BASE_URL: &str = "https://api.openweathermap.org";

/// The OpenWeather APIs that can supply a "feels like" forecast.
#[derive(Clone, Copy, Debug, PartialEq, Eq, clap::ValueEnum)]
pub enum Api {
    /// The free 5 day / 3 hour forecast; works with any API key.
    Forecast,
    /// The One Call API 3.0 hourly forecast; needs a One Call subscription.
    Onecall,
}

impl Api {
    pub fn provider_name(self) -> &'static str {
        match self {
            Api::Forecast => "openweather-forecast-2.5",
            Api::Onecall => "openweather-onecall-3.0",
        }
    }

    fn path(self) -> &'static str {
        match self {
            Api::Forecast => "/data/2.5/forecast",
            Api::Onecall => "/data/3.0/onecall",
        }
    }
}

/// A single forecast point as OpenWeather reports it.
#[derive(Debug, Clone, PartialEq)]
pub struct Sample {
    pub timestamp: i64,
    pub feels_like_celsius: f64,
}

#[derive(Deserialize)]
struct ForecastResponse {
    list: Vec<ForecastEntry>,
}

#[derive(Deserialize)]
struct ForecastEntry {
    dt: i64,
    main: ForecastMain,
}

#[derive(Deserialize)]
struct ForecastMain {
    feels_like: f64,
}

#[derive(Deserialize)]
struct OneCallResponse {
    hourly: Vec<OneCallHour>,
}

#[derive(Deserialize)]
struct OneCallHour {
    dt: i64,
    feels_like: f64,
}

#[derive(Deserialize)]
struct ErrorResponse {
    message: String,
}

/// Fetches the forecast for a location, in degrees Celsius.
pub async fn fetch(
    client: &reqwest::Client,
    base_url: &str,
    api: Api,
    lat: f64,
    lon: f64,
    api_key: &str,
) -> Result<Vec<Sample>> {
    let mut query = vec![
        ("lat", lat.to_string()),
        ("lon", lon.to_string()),
        ("units", "metric".to_string()),
        ("appid", api_key.to_string()),
    ];
    if api == Api::Onecall {
        query.push(("exclude", "current,minutely,daily,alerts".to_string()));
    }

    // reqwest puts the URL in its errors, and the URL holds the API key, so
    // strip it before the error can reach a log.
    let response = client
        .get(format!("{}{}", base_url.trim_end_matches('/'), api.path()))
        .query(&query)
        .send()
        .await
        .map_err(|e| e.without_url())
        .context("calling OpenWeather")?;

    let status = response.status();
    let body = response
        .text()
        .await
        .map_err(|e| e.without_url())
        .context("reading the OpenWeather response")?;

    if !status.is_success() {
        let message = serde_json::from_str::<ErrorResponse>(&body)
            .map(|e| e.message)
            .unwrap_or(body);
        if api == Api::Onecall && status == reqwest::StatusCode::UNAUTHORIZED {
            bail!("OpenWeather returned {status}: {message} (One Call 3.0 needs its own subscription; --api forecast works with a free key)");
        }
        bail!("OpenWeather returned {status}: {message}");
    }

    parse(api, &body)
}

/// Parses a successful response body from the given API.
pub fn parse(api: Api, body: &str) -> Result<Vec<Sample>> {
    let samples: Vec<Sample> = match api {
        Api::Forecast => serde_json::from_str::<ForecastResponse>(body)
            .context("parsing the 5 day forecast response")?
            .list
            .into_iter()
            .map(|e| Sample {
                timestamp: e.dt,
                feels_like_celsius: e.main.feels_like,
            })
            .collect(),
        Api::Onecall => serde_json::from_str::<OneCallResponse>(body)
            .context("parsing the One Call response")?
            .hourly
            .into_iter()
            .map(|h| Sample {
                timestamp: h.dt,
                feels_like_celsius: h.feels_like,
            })
            .collect(),
    };
    ensure!(!samples.is_empty(), "OpenWeather returned no forecast points");
    Ok(samples)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::sync::mpsc;
    use std::thread;

    const FORECAST_BODY: &str = r#"{
        "cod": "200", "message": 0, "cnt": 2,
        "list": [
            {"dt": 1730000400, "main": {"temp": 11.2, "feels_like": 10.1, "humidity": 80}},
            {"dt": 1730011200, "main": {"temp": 9.0, "feels_like": 7.4, "humidity": 85}}
        ],
        "city": {"name": "London"}
    }"#;

    const ONECALL_BODY: &str = r#"{
        "lat": 51.5074, "lon": -0.1278, "timezone": "Europe/London",
        "hourly": [
            {"dt": 1730000400, "temp": 11.2, "feels_like": 10.1},
            {"dt": 1730004000, "temp": 10.8, "feels_like": 9.6}
        ]
    }"#;

    #[test]
    fn parses_forecast_response() {
        let samples = parse(Api::Forecast, FORECAST_BODY).unwrap();
        assert_eq!(
            samples,
            vec![
                Sample {
                    timestamp: 1730000400,
                    feels_like_celsius: 10.1
                },
                Sample {
                    timestamp: 1730011200,
                    feels_like_celsius: 7.4
                },
            ]
        );
    }

    #[test]
    fn parses_onecall_response() {
        let samples = parse(Api::Onecall, ONECALL_BODY).unwrap();
        assert_eq!(samples.len(), 2);
        assert_eq!(
            samples[1],
            Sample {
                timestamp: 1730004000,
                feels_like_celsius: 9.6
            }
        );
    }

    #[test]
    fn rejects_empty_forecast() {
        let err = parse(Api::Forecast, r#"{"list": []}"#).unwrap_err();
        assert!(err.to_string().contains("no forecast points"));
    }

    #[test]
    fn rejects_malformed_body() {
        assert!(parse(Api::Onecall, r#"{"current": {}}"#).is_err());
    }

    /// Serves one canned HTTP response and returns the request line it saw.
    fn serve_once(status: &str, body: &'static str) -> (String, mpsc::Receiver<String>) {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let status = status.to_string();
        let (tx, rx) = mpsc::channel();
        thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut buf = [0u8; 4096];
            let n = stream.read(&mut buf).unwrap();
            let request = String::from_utf8_lossy(&buf[..n]).to_string();
            tx.send(request.lines().next().unwrap_or_default().to_string()).unwrap();
            let response = format!(
                "HTTP/1.1 {status}\r\ncontent-type: application/json\r\ncontent-length: {}\r\nconnection: close\r\n\r\n{body}",
                body.len()
            );
            stream.write_all(response.as_bytes()).unwrap();
        });
        (format!("http://{addr}"), rx)
    }

    #[tokio::test]
    async fn fetch_sends_query_and_parses_response() {
        let (base, request) = serve_once("200 OK", FORECAST_BODY);
        let client = reqwest::Client::new();

        let samples = fetch(&client, &base, Api::Forecast, 51.5074, -0.1278, "secret")
            .await
            .unwrap();

        assert_eq!(samples.len(), 2);
        let line = request.recv().unwrap();
        assert!(line.starts_with("GET /data/2.5/forecast?"), "{line}");
        assert!(line.contains("lat=51.5074"), "{line}");
        assert!(line.contains("lon=-0.1278"), "{line}");
        assert!(line.contains("units=metric"), "{line}");
        assert!(line.contains("appid=secret"), "{line}");
    }

    #[tokio::test]
    async fn fetch_reports_api_errors_without_leaking_the_key() {
        let (base, _request) = serve_once("401 Unauthorized", r#"{"cod":401,"message":"Invalid API key."}"#);
        let client = reqwest::Client::new();

        let err = fetch(&client, &base, Api::Onecall, 51.5, -0.1, "secret")
            .await
            .unwrap_err();

        let text = format!("{err:#}");
        assert!(text.contains("401"), "{text}");
        assert!(text.contains("Invalid API key."), "{text}");
        assert!(text.contains("--api forecast"), "{text}");
        assert!(!text.contains("secret"), "{text}");
    }

    #[tokio::test]
    async fn connection_errors_do_not_leak_the_key() {
        // Bind then drop a listener so the port is very likely closed.
        let addr = TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap();
        let client = reqwest::Client::new();

        let err = fetch(&client, &format!("http://{addr}"), Api::Forecast, 51.5, -0.1, "secret")
            .await
            .unwrap_err();

        assert!(!format!("{err:#}").contains("secret"));
    }
}
