// control-plane
package main

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/go-redis/redis/v8"
	_ "github.com/lib/pq"
	"golang.org/x/net/context"
)

// Global variables for the previous status
var prevWorkers []string
var prevRecordIDs []string

var ctx = context.Background()
var db *sql.DB

func setupDatabase() {
	connStr := fmt.Sprintf("host=%s user=%s password=%s dbname=%s sslmode=disable",
		os.Getenv("POSTGRES_HOST"),
		os.Getenv("POSTGRES_USER"),
		os.Getenv("POSTGRES_PASSWORD"),
		os.Getenv("POSTGRES_DB"))

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("Failed to connect to the database: %v", err)
	}

	if err = db.Ping(); err != nil {
		log.Fatalf("Failed to ping the database: %v", err)
	}

	var tableName string
	err = db.QueryRow("SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'work_items'").Scan(&tableName)
	if err != nil && err != sql.ErrNoRows {
		log.Fatalf("Failed to check for existing table: %v", err)
	}

	if tableName == "work_items" {
		_, err = db.Exec("DROP TABLE work_items")
		if err != nil {
			log.Fatalf("Failed to drop existing table: %v", err)
		}
	}

	_, err = db.Exec("CREATE EXTENSION IF NOT EXISTS \"pgcrypto\";")
	if err != nil {
		log.Fatalf("Failed to enable pgcrypto extension: %v", err)
	}

	_, err = db.Exec(`
        CREATE TABLE work_items (
			id UUID DEFAULT gen_random_uuid() PRIMARY KEY,
			value INTEGER DEFAULT 0,
			currentWorker VARCHAR(255) DEFAULT NULL
        )
    `)
	if err != nil {
		log.Fatalf("Failed to create new table: %v", err)
	}

	for i := 1; i <= 5; i++ {
		_, err := db.Exec("INSERT INTO work_items (value) VALUES (DEFAULT)")
		if err != nil {
			log.Printf("Failed to insert record %d: %v", i, err)
		}
	}
	log.Printf("Successfully inserted 5 sample record into work_items table")
}

func connectToRedis() *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "redis-service:6379",
		Password: "",
		DB:       0,
	})

	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	return rdb
}

func evalSystem(rdb *redis.Client) {
	// Variables for the current status
	var curWorkers []string
	var curRecordIDs []string

	// Query the IDs of records in the work_items table
	rows, err := db.Query("SELECT id FROM work_items")
	if err != nil {
		log.Printf("Failed to fetch IDs from the database: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			log.Printf("Failed to scan ID from the row: %v", err)
			return
		}
		curRecordIDs = append(curRecordIDs, id)
	}

	// Fetch the worker keys from Redis hash
	curWorkers = rdb.HKeys(ctx, "workers").Val()

	// Print system status
	log.Printf("")
	fmt.Println("----------------------------------------")
	fmt.Println("|            System Status             |")
	fmt.Println("|--------------------------------------|")
	fmt.Println("|              |  Current  |  Previous |")
	fmt.Println("|--------------------------------------|")
	fmt.Printf("| Running Pods |    %3d    |    %3d    |\n", len(curWorkers), len(prevWorkers))
	fmt.Printf("| Records Count|    %3d    |    %3d    |\n", len(curRecordIDs), len(prevRecordIDs))
	fmt.Println("|--------------------------------------|")

	if len(curWorkers) == len(prevWorkers) && len(curRecordIDs) == len(prevRecordIDs) {
		fmt.Println("| No change in status.                 |")
		fmt.Println("----------------------------------------")
	} else {
		fmt.Println("| Change detected, rehashing started!  |")
		fmt.Println("----------------------------------------")
		rehash(rdb, curWorkers, curRecordIDs)
	}
	fmt.Println()

	// Update global variables for the previous status
	prevWorkers = curWorkers
	prevRecordIDs = curRecordIDs
}

func rehash(rdb *redis.Client, curWorkers []string, curRecordIDs []string) {
	// Convert the IDs to a string representation
	idStr := fmt.Sprintf("%v", curRecordIDs)
	log.Printf("Converted IDs to string: %s", idStr)

	// Update the list of IDs for each worker in the Redis hash
	log.Println("Updating Redis hash")
	for _, worker := range curWorkers {
		rdb.HSet(ctx, "workers", worker, idStr)
	}
	log.Println("Updated Redis hash successfully")
}

// Hashing function
func hashString(s string) string {
	h := sha1.New()
	h.Write([]byte(s))
	sha1Hash := hex.EncodeToString(h.Sum(nil))
	return sha1Hash
}

func rehash(rdb *redis.Client, curWorkers []string, curRecordIDs []string) {
	hashRing := make(map[string]string)
	sortedKeys := []string{}

	// Hash each worker and put it on the hash ring
	for _, worker := range curWorkers {
		hash := hashString(worker)
		hashRing[hash] = worker
		sortedKeys = append(sortedKeys, hash)
	}

	// Sort the keys to iterate over the hash ring in order
	sort.Strings(sortedKeys)

	// Distribute record IDs across workers
	distribution := make(map[string][]string)
	for _, id := range curRecordIDs {
		hash := hashString(id)
		// Find the appropriate worker for the hash
		for _, key := range sortedKeys {
			if hash <= key {
				distribution[hashRing[key]] = append(distribution[hashRing[key]], id)
				break
			}
		}
		// If hash is larger than any worker hash, assign to the first worker in the sorted list
		if _, exists := distribution[hash]; !exists {
			distribution[hashRing[sortedKeys[0]]] = append(distribution[hashRing[sortedKeys[0]]], id)
		}
	}

	// Update Redis to reflect the new distribution
	log.Println("Updating Redis hash")
	for worker, ids := range distribution {
		idStr := fmt.Sprintf("%v", ids)
		rdb.HSet(ctx, "workers", worker, idStr)
	}
	log.Println("Updated Redis hash successfully")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Healthy!")
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Ready!")
}

func main() {
	log.Println("Starting control-plane application...")
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/ready", readinessHandler)

	setupDatabase()

	rdb := connectToRedis()

	// Create a new ticker that triggers every 5 seconds
	ticker := time.NewTicker(5 * time.Second)

	go func() {
		for {
			select {
			case <-ticker.C:
				// Update and print system status every 5 seconds
				evalSystem(rdb)
			}
		}
	}()

	http.ListenAndServe(":8080", nil)
}
