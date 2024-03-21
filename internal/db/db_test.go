package db

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	"go.etcd.io/bbolt"
)

func TestNewDB(t *testing.T) {
	dbPath := "test.db"
	db, err := NewDB(dbPath)
	defer os.Remove(dbPath)

	if err != nil {
		t.Fatalf("Failed to create DB: %v", err)
	}

	if db == nil {
		t.Fatal("Expected non-nil DB, got nil")
	}
}

func TestUpdateTemperatures(t *testing.T) {
	dbPath := "test.db"
	db, _ := NewDB(dbPath)
	defer os.Remove(dbPath)

	otemps := make([]float64, 24)
	itemps := make([]float64, 24)
	for i := range otemps {
		otemps[i] = float64(i)
		itemps[i] = float64(i)
	}

	err := db.UpdateTemperatures(otemps, itemps)
	if err != nil {
		t.Fatalf("Failed to update temperatures: %v", err)
	}

	err = db.db.View(func(tx *bbolt.Tx) error {
		otempBucket := tx.Bucket([]byte("otemp"))
		itempBucket := tx.Bucket([]byte("itemp"))

		now := time.Now()
		for i := 0; i < 24; i++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, uint64(now.Add(time.Duration(i)*time.Hour).Unix()))

			otemp := otempBucket.Get(key)
			if otemp == nil || bytesToFloat64(otemp) != float64(i) {
				return errors.New("otemp data mismatch")
			}

			itemp := itempBucket.Get(key)
			if itemp == nil || bytesToFloat64(itemp) != float64(i) {
				return errors.New("itemp data mismatch")
			}
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Data verification failed: %v", err)
	}
}

// func bytesToFloat64(b []byte) float64 {
// 	return math.Float64frombits(binary.BigEndian.Uint64(b))
// }
