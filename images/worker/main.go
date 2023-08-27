package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	_ "github.com/lib/pq"
	"golang.org/x/net/context"
)

var ctx = context.Background()
var rdb *redis.Client
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

func registerWorkerToRedis(rdb *redis.Client) {
	podName := os.Getenv("POD_NAME")
	rdb.HSet(ctx, "workers", podName, "[]")
}

func deregisterWorkerFromRedis(rdb *redis.Client) {
	podName := os.Getenv("POD_NAME")
	rdb.HDel(ctx, "workers", podName)
	log.Printf("Deregistered worker: %s", podName)
}

func connectToRedis() {
	rdb = redis.NewClient(&redis.Options{
		Addr:     "redis-service:6379",
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
}

func updateDatabase() {
	podName := os.Getenv("POD_NAME")

	for {
		time.Sleep(1 * time.Second)
		log.Printf("Starting update at: %s", time.Now())

		// Retrieve IDs from Redis
		idStr := rdb.HGet(ctx, "workers", podName).Val()
		if idStr != "" {
			log.Printf("Worker %s is processing IDs: %s", podName, idStr)

			// Split the ID string into an array, separating by space
			ids := strings.Fields(idStr)

			for _, id := range ids {
				id = strings.TrimSpace(id)
				id = strings.Trim(id, "[]")

				result, err := db.Exec("UPDATE work_items SET value = value + 1, currentWorker = $1 WHERE id = $2", podName, id)

				if err != nil {
					log.Printf("Failed to update database record with ID %s: %v", id, err)
				} else {
					affected, _ := result.RowsAffected()
					log.Printf("Worker processed %d row with ID: %s", affected, id)
				}
			}
			log.Printf("Finished update at: %s", time.Now())
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
	log.Println("Starting worker application...")
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/ready", readinessHandler)

	setupDatabase()
	connectToRedis()
	registerWorkerToRedis(rdb)

	// Create a channel to listen for system signals
	sigChan := make(chan os.Signal, 1)
	// Register for SIGINT (Ctrl+C) and SIGTERM (termination by system) signals
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start a goroutine that listens on sigChan
	go func() {
		sig := <-sigChan // Block until a signal is received
		log.Printf("Received signal: %v. Deregistering worker...", sig)
		deregisterWorkerFromRedis(rdb)
		os.Exit(0)
	}()

	go updateDatabase()

	http.ListenAndServe(":8080", nil)
}
