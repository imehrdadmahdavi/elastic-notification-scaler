package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

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

	// Check if the worker is already registered
	exists := rdb.SIsMember(ctx, "workers", podName).Val()
	if !exists {
		// Add worker name to the set
		rdb.SAdd(ctx, "workers", podName)

		// Register worker in the stream with status RUNNING and no IDs
		rdb.XAdd(ctx, &redis.XAddArgs{
			Stream: "worker-status",
			Values: map[string]interface{}{"name": podName, "status": "RUNNING", "ids": "[]"},
		})
	}
}

func deregisterWorkerFromRedis(rdb *redis.Client) {
	podName := os.Getenv("POD_NAME")
	// Remove the worker name from the Redis set
	rdb.SRem(ctx, "workers", podName)
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

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Healthy!")
}

func readinessHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Ready!")
}

func updateDatabase() {
	podName := os.Getenv("POD_NAME")

	// Infinite loop to listen to new messages from Redis stream
	for {
		xreadResp, err := rdb.XRead(ctx, &redis.XReadArgs{
			Streams: []string{"worker-status", "0"},
			Count:   10,
		}).Result()
		if err != nil {
			log.Printf("Failed to read from Redis stream: %v", err)
			continue
		}

		for _, xMessage := range xreadResp[0].Messages {
			for _, xValue := range xMessage.Values {
				if id, ok := xValue.(int); ok {
					// Update the database record with the given ID
					_, err := db.Exec("UPDATE work_items SET value = value + 1, currentworker = $1 WHERE id = $2", podName, id)
					if err != nil {
						log.Printf("Failed to update database record with ID %d: %v", id, err)
					}
				}
			}
		}
	}
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
