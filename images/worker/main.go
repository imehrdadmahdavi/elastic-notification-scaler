package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/go-redis/redis/v8"
	"golang.org/x/net/context"
)

var ctx = context.Background()
var rdb *redis.Client

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

func main() {
	log.Println("Starting worker application...")
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/ready", readinessHandler)

	connectToRedis()

	// Register worker status in Redis
	podName := os.Getenv("POD_NAME")
	_, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "worker-status",
		Values: map[string]interface{}{"message": fmt.Sprintf("STARTED:%s", podName)},
	}).Result()
	if err != nil {
		log.Fatalf("Failed to send worker status to Redis: %v", err)
	}

	http.ListenAndServe(":8080", nil)
}
