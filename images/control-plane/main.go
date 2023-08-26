// control-plane
package main

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
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

	if reflect.DeepEqual(curWorkers, prevWorkers) && reflect.DeepEqual(curRecordIDs, prevRecordIDs) {
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

// Hashing function
func hashString(s string) uint32 {
	h := sha1.New()
	h.Write([]byte(s))
	sha1Hash := h.Sum(nil)

	// We only take the first 4 bytes as the hash value
	return uint32(sha1Hash[0])<<24 | uint32(sha1Hash[1])<<16 | uint32(sha1Hash[2])<<8 | uint32(sha1Hash[3])
}

func rehash(rdb *redis.Client, curWorkers []string, curRecordIDs []string) {
	if len(curWorkers) == 0 {
		// Handle zero workers case
		log.Println("No workers are available. Clearing Redis hash.")
		rdb.Del(ctx, "workers")
		return
	}

	// Hash ring to hold both workers and IDs
	hashRing := make(map[uint32]string)
	var sortedKeys []uint32

	// Number of virtual nodes for each worker
	numVirtualNodes := 10

	// Hash each worker and its virtual nodes, and put them on the hash ring
	for _, worker := range curWorkers {
		for i := 0; i < numVirtualNodes; i++ {
			virtualNode := fmt.Sprintf("%s#%d", worker, i)
			hash := hashString(virtualNode)
			hashRing[hash] = worker // point back to the original worker
			sortedKeys = append(sortedKeys, hash)
		}
	}

	// Sort the keys to create an ordered ring
	sort.Slice(sortedKeys, func(i, j int) bool {
		return sortedKeys[i] < sortedKeys[j]
	})

	// Initialize a map to hold the distribution of IDs across workers
	distribution := make(map[string][]string)

	// Distribute IDs to the closest worker on the ring
	for _, id := range curRecordIDs {
		hash := hashString(id)
		var targetWorker string

		for _, workerHash := range sortedKeys {
			if hash <= workerHash {
				targetWorker = hashRing[workerHash]
				break
			}
		}

		// If hash is greater than any worker hash, assign to the first worker
		if targetWorker == "" {
			targetWorker = hashRing[sortedKeys[0]]
		}

		distribution[targetWorker] = append(distribution[targetWorker], id)
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
