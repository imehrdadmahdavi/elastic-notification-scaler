package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-redis/redis/v8"
	_ "github.com/lib/pq"
	"golang.org/x/net/context"
)

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

	// Check if table 'work_items' exists
	var tableName string
	err = db.QueryRow("SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'work_items'").Scan(&tableName)
	if err != nil && err != sql.ErrNoRows {
		log.Fatalf("Failed to check for existing table: %v", err)
	}

	// Drop table if it exists
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

	// Create new table 'work_items' with updated schema
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

	// Insert sample data
	for i := 1; i <= 5; i++ {
		// Inserting with default values for UUID and currentWorker, and value set to 0
		_, err := db.Exec("INSERT INTO work_items (value) VALUES (DEFAULT)")
		if err != nil {
			log.Printf("Failed to insert record %d: %v", i, err)
		} else {
			log.Printf("Successfully inserted record %d", i)
		}
	}
}

func connectToRedis() *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     "redis-service:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	return rdb
}

func printInfo(rdb *redis.Client) {
	for {
		time.Sleep(5 * time.Second)

		// Query the number of records in the work_items table
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM work_items").Scan(&count)
		if err != nil {
			log.Printf("Failed to count records in the table: %v", err)
		}

		// Fetch the number of worker pods from Redis
		workerCount := rdb.SCard(ctx, "workers").Val() // Use the set to get the count of workers

		log.Printf("Records in work_items table: %d", count)
		log.Printf("Number of running worker pods: %d", workerCount)
	}
}

func sendIDsToRedis(rdb *redis.Client) {
	// 1. Query all IDs from the work_items table
	rows, err := db.Query("SELECT id FROM work_items")
	if err != nil {
		log.Printf("Failed to fetch IDs from the database: %v", err)
		return
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			log.Printf("Failed to scan ID from the row: %v", err)
			return
		}
		ids = append(ids, id)
	}

	// Convert the list of IDs to a comma-separated string for easier handling
	idStr := fmt.Sprintf("[%v]", ids)

	// 2. Iterate over the worker entries in the Redis set
	workers := rdb.SMembers(ctx, "workers").Val()
	for _, worker := range workers {
		// 3. Update the list of IDs for each worker in the Redis stream
		_, err = rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: "worker-status",
			Values: map[string]interface{}{"name": worker, "ids": idStr},
		}).Result()

		if err != nil {
			log.Printf("Failed to update IDs for worker %s in Redis: %v", worker, err)
			continue
		}
	}
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

	// I commented out this line since the function isn't defined in the provided code.
	// sendIDsToRedis(rdb)

	go printInfo(rdb)

	http.ListenAndServe(":8080", nil)
}
