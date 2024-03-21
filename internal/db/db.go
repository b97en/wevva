package db

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"time"

	"go.etcd.io/bbolt"
)

type DB struct {
	db *bbolt.DB
}

func NewDB(dbPath string) (*DB, error) {
	db, err := bbolt.Open(dbPath, 0666, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, err
	}

	// Create buckets
	err = db.Update(func(tx *bbolt.Tx) error {
		buckets := []string{"otemp_today", "otemp_yday", "itemp_today", "itemp_yday"}
		for _, bucket := range buckets {
			_, err := tx.CreateBucketIfNotExists([]byte(bucket))
			if err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		db.Close()
		return nil, err
	}

	return &DB{db: db}, nil
}

func (d *DB) UpdateTemperatureData(otempsToday, otempsYday, itempsToday, itempsYday []float64, timestamps []time.Time) error {
	return d.db.Update(func(tx *bbolt.Tx) error {
		// Retrieve buckets for each category
		otempTodayBucket := tx.Bucket([]byte("otemp_today"))
		otempYdayBucket := tx.Bucket([]byte("otemp_yday"))
		itempTodayBucket := tx.Bucket([]byte("itemp_today"))
		itempYdayBucket := tx.Bucket([]byte("itemp_yday"))

		// Helper function to update a specific bucket
		updateBucket := func(bucket *bbolt.Bucket, temps []float64, timestamps []time.Time) error {
			for i, temp := range temps {
				key := make([]byte, 8)
				binary.BigEndian.PutUint64(key, uint64(timestamps[i].Unix()))
				if err := bucket.Put(key, float64ToBytes(temp)); err != nil {
					return err
				}
			}
			return nil
		}

		// Update each bucket with the corresponding data
		if err := updateBucket(otempTodayBucket, otempsToday, timestamps); err != nil {
			return err
		}
		if err := updateBucket(otempYdayBucket, otempsYday, timestamps); err != nil {
			return err
		}
		if err := updateBucket(itempTodayBucket, itempsToday, timestamps); err != nil {
			return err
		}
		if err := updateBucket(itempYdayBucket, itempsYday, timestamps); err != nil {
			return err
		}

		return nil
	})
}

// ViewBucketValues prints all key-value pairs in the specified buckets.
// This is a generic read operation that can be tailored for specific needs.
func (d *DB) ViewBucketValues(bucketNames []string) error {
	return d.db.View(func(tx *bbolt.Tx) error {
		for _, bucketName := range bucketNames {
			b := tx.Bucket([]byte(bucketName))
			if b == nil {
				log.Printf("Bucket %s not found!", bucketName)
				continue // Skip to the next bucket if this one doesn't exist
			}
			fmt.Printf("Contents of bucket '%s':\n", bucketName)
			err := b.ForEach(func(k, v []byte) error {
				// Assuming the value is stored as a float64; adjust this based on your actual data format
				val := bytesToFloat64(v)
				fmt.Printf("Key: %v, Value: %f\n", binary.BigEndian.Uint64(k), val)
				return nil
			})
			if err != nil {
				return fmt.Errorf("error reading bucket %s: %w", bucketName, err)
			}
		}
		return nil
	})
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
