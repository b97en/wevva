// Package db keeps wevva's runtime state in a bbolt file: the latest hourly
// outdoor forecast and a history of indoor observations.
package db

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"go.etcd.io/bbolt"
)

var (
	forecastBucket    = []byte("forecast")
	observationBucket = []byte("observations")
)

// HourlyTemp is the forecast outdoor "feels like" temperature for one hour.
type HourlyTemp struct {
	Time      time.Time
	FeelsLike float64
}

// Observation is what the agent saw on one run.
type Observation struct {
	Time     time.Time `json:"-"`
	Indoor   float64   `json:"indoor"`
	Outdoor  float64   `json:"outdoor"`
	Heating  bool      `json:"heating"`
	Setpoint float64   `json:"setpoint"`
	Mode     string    `json:"mode"`
}

type DB struct {
	db *bbolt.DB
}

// Open opens (or creates) the database at path. It waits up to timeout for
// another process to release the file.
func Open(path string, timeout time.Duration) (*DB, error) {
	b, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: timeout})
	if errors.Is(err, bbolt.ErrTimeout) {
		return nil, fmt.Errorf("open %s: another process has it locked", path)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	err = b.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{forecastBucket, observationBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	return &DB{db: b}, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

// PutForecast stores forecast hours, replacing any earlier forecast for the
// same hours.
func (d *DB) PutForecast(hours []HourlyTemp) error {
	return d.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket(forecastBucket)
		for _, h := range hours {
			if err := b.Put(timeKey(h.Time), float64ToBytes(h.FeelsLike)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Forecast returns the stored forecast hours in [from, to), oldest first.
func (d *DB) Forecast(from, to time.Time) ([]HourlyTemp, error) {
	var hours []HourlyTemp
	err := d.db.View(func(tx *bbolt.Tx) error {
		return scan(tx.Bucket(forecastBucket), from, to, func(t time.Time, v []byte) error {
			if len(v) != 8 {
				return fmt.Errorf("forecast at %s: value is %d bytes, want 8", t, len(v))
			}
			hours = append(hours, HourlyTemp{Time: t, FeelsLike: bytesToFloat64(v)})
			return nil
		})
	})
	return hours, err
}

// PutObservation stores an observation, keyed by its time to the second.
func (d *DB) PutObservation(o Observation) error {
	v, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return d.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(observationBucket).Put(timeKey(o.Time), v)
	})
}

// Observations returns the observations in [from, to), oldest first.
func (d *DB) Observations(from, to time.Time) ([]Observation, error) {
	var obs []Observation
	err := d.db.View(func(tx *bbolt.Tx) error {
		return scan(tx.Bucket(observationBucket), from, to, func(t time.Time, v []byte) error {
			var o Observation
			if err := json.Unmarshal(v, &o); err != nil {
				return fmt.Errorf("observation at %s: %w", t, err)
			}
			o.Time = t
			obs = append(obs, o)
			return nil
		})
	})
	return obs, err
}

// Prune deletes forecast hours and observations from before cutoff.
func (d *DB) Prune(cutoff time.Time) error {
	return d.db.Update(func(tx *bbolt.Tx) error {
		for _, name := range [][]byte{forecastBucket, observationBucket} {
			c := tx.Bucket(name).Cursor()
			end := timeKey(cutoff)
			for k, _ := c.First(); k != nil && bytes.Compare(k, end) < 0; k, _ = c.First() {
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// scan calls fn for each key in [from, to). Keys are big-endian Unix
// seconds, so byte order is time order.
func scan(b *bbolt.Bucket, from, to time.Time, fn func(time.Time, []byte) error) error {
	c := b.Cursor()
	end := timeKey(to)
	for k, v := c.Seek(timeKey(from)); k != nil && bytes.Compare(k, end) < 0; k, v = c.Next() {
		if len(k) != 8 {
			continue
		}
		if err := fn(keyTime(k), v); err != nil {
			return err
		}
	}
	return nil
}

func timeKey(t time.Time) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, uint64(t.Unix()))
	return key
}

func keyTime(k []byte) time.Time {
	return time.Unix(int64(binary.BigEndian.Uint64(k)), 0)
}

func float64ToBytes(f float64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, math.Float64bits(f))
	return buf
}

// bytesToFloat64 converts a byte slice to a float64.
// This assumes that the byte slice represents a float64 value stored in big-endian order.
func bytesToFloat64(b []byte) float64 {
	bits := binary.BigEndian.Uint64(b)
	return math.Float64frombits(bits)
}
