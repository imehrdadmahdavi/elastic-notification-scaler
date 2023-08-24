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
		workerCount := rdb.XLen(ctx, "worker-status").Val()

		log.Printf("Records in work_items table: %d", count)
		log.Printf("Number of running worker pods: %d", workerCount)
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

	go printInfo(rdb)

	http.ListenAndServe(":8080", nil)
}
