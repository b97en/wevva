//! Resamples OpenWeather samples onto whole hours and defines the JSON
//! report that the Go agent reads.

use anyhow::{ensure, Result};
use serde::Serialize;

use crate::openweather::Sample;

const HOUR: i64 = 3600;

/// The JSON document printed on stdout.
#[derive(Serialize, Debug)]
pub struct Report {
    pub provider: &'static str,
    pub latitude: f64,
    pub longitude: f64,
    pub generated_at: i64,
    pub hours: Vec<HourlyTemperature>,
}

#[derive(Serialize, Debug, Clone, PartialEq)]
pub struct HourlyTemperature {
    pub timestamp: i64,
    pub feels_like_celsius: f64,
}

/// Returns one value per hour for `hours` hours, starting at the top of the
/// hour that contains `now`.
///
/// The free forecast API only has a point every three hours, so values in
/// between are linearly interpolated. Hours before the first point or after
/// the last one take the nearest point's value.
pub fn hourly(samples: &[Sample], now: i64, hours: u32) -> Result<Vec<HourlyTemperature>> {
    ensure!(!samples.is_empty(), "no forecast points to resample");

    let mut sorted = samples.to_vec();
    sorted.sort_by_key(|s| s.timestamp);

    let start = now - now.rem_euclid(HOUR);
    Ok((0..i64::from(hours))
        .map(|h| {
            let timestamp = start + h * HOUR;
            HourlyTemperature {
                timestamp,
                feels_like_celsius: round2(interpolate(&sorted, timestamp)),
            }
        })
        .collect())
}

/// Linearly interpolates sorted samples at time `t`, holding the end values
/// flat outside their range.
fn interpolate(sorted: &[Sample], t: i64) -> f64 {
    let i = sorted.partition_point(|s| s.timestamp <= t);
    if i == 0 {
        return sorted[0].feels_like_celsius;
    }
    if i == sorted.len() {
        return sorted[i - 1].feels_like_celsius;
    }
    let (a, b) = (&sorted[i - 1], &sorted[i]);
    let frac = (t - a.timestamp) as f64 / (b.timestamp - a.timestamp) as f64;
    a.feels_like_celsius + (b.feels_like_celsius - a.feels_like_celsius) * frac
}

fn round2(v: f64) -> f64 {
    (v * 100.0).round() / 100.0
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample(timestamp: i64, feels_like_celsius: f64) -> Sample {
        Sample {
            timestamp,
            feels_like_celsius,
        }
    }

    const T0: i64 = 1_730_001_600; // a whole hour

    #[test]
    fn starts_at_the_current_hour() {
        let samples = [sample(T0, 10.0), sample(T0 + 3 * HOUR, 7.0)];

        let hours = hourly(&samples, T0 + 25 * 60, 4).unwrap();

        let stamps: Vec<i64> = hours.iter().map(|h| h.timestamp).collect();
        assert_eq!(stamps, vec![T0, T0 + HOUR, T0 + 2 * HOUR, T0 + 3 * HOUR]);
    }

    #[test]
    fn interpolates_three_hourly_points() {
        let samples = [sample(T0, 10.0), sample(T0 + 3 * HOUR, 7.0)];

        let temps: Vec<f64> = hourly(&samples, T0, 4)
            .unwrap()
            .iter()
            .map(|h| h.feels_like_celsius)
            .collect();

        assert_eq!(temps, vec![10.0, 9.0, 8.0, 7.0]);
    }

    #[test]
    fn holds_end_values_outside_the_data() {
        let samples = [sample(T0 + HOUR, 5.0), sample(T0 + 2 * HOUR, 6.0)];

        let temps: Vec<f64> = hourly(&samples, T0, 4)
            .unwrap()
            .iter()
            .map(|h| h.feels_like_celsius)
            .collect();

        assert_eq!(temps, vec![5.0, 5.0, 6.0, 6.0]);
    }

    #[test]
    fn sorts_unordered_samples() {
        let samples = [sample(T0 + 2 * HOUR, 8.0), sample(T0, 4.0)];

        let temps: Vec<f64> = hourly(&samples, T0, 3)
            .unwrap()
            .iter()
            .map(|h| h.feels_like_celsius)
            .collect();

        assert_eq!(temps, vec![4.0, 6.0, 8.0]);
    }

    #[test]
    fn rounds_to_two_decimal_places() {
        let samples = [sample(T0, 10.0), sample(T0 + 3 * HOUR, 9.0)];

        let hours = hourly(&samples, T0, 2).unwrap();

        assert_eq!(hours[1].feels_like_celsius, 9.67);
    }

    #[test]
    fn rejects_no_samples() {
        assert!(hourly(&[], T0, 12).is_err());
    }

    #[test]
    fn report_serialises_to_the_agreed_shape() {
        let report = Report {
            provider: "openweather-forecast-2.5",
            latitude: 51.5,
            longitude: -0.12,
            generated_at: T0,
            hours: vec![HourlyTemperature {
                timestamp: T0,
                feels_like_celsius: 9.5,
            }],
        };

        let json: serde_json::Value = serde_json::to_value(&report).unwrap();

        assert_eq!(
            json,
            serde_json::json!({
                "provider": "openweather-forecast-2.5",
                "latitude": 51.5,
                "longitude": -0.12,
                "generated_at": T0,
                "hours": [{"timestamp": T0, "feels_like_celsius": 9.5}]
            })
        );
    }
}
