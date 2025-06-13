package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

func connectToDatabase() {
	dbHost := os.Getenv("DB_HOST")
	dbPort := os.Getenv("DB_PORT")
	dbUser := os.Getenv("DB_USERNAME")
	dbPassword := os.Getenv("DB_PASSWORD")
	dbName := os.Getenv("DB_NAME")

	if dbHost == "" || dbUser == "" || dbPassword == "" || dbName == "" {
		log.Fatal("Missing required database environment variables")
	}

	if dbPort == "" {
		dbPort = "5432"
	}

	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		dbHost, dbPort, dbUser, dbPassword, dbName)

	fmt.Printf("Attempting to connect to database at %s:%s...\n", dbHost, dbPort)

	for {
		var err error
		db, err = sql.Open("postgres", connStr)
		if err != nil {
			log.Printf("Failed to open database connection: %v. Retrying in 1 seconds...", err)
			time.Sleep(1 * time.Second)
			continue
		}

		err = db.Ping()
		if err != nil {
			log.Printf("Failed to ping database: %v. Retrying in 1 seconds...", err)
			db.Close()
			time.Sleep(1 * time.Second)
			continue
		}

		fmt.Println("✅ Successfully connected to database!")
		break
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if db == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Database not connected"))
		return
	}

	err := db.Ping()
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("Database connection failed"))
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func main() {
	fmt.Println("Starting tenant service...")

	connectToDatabase()
	defer db.Close()

	http.HandleFunc("/healthz", healthHandler)

	port := "8080"
	fmt.Printf("🚀 Server ready on port %s\n", port)
	fmt.Printf("Health endpoint: http://localhost:%s/healthz\n", port)

	log.Fatal(http.ListenAndServe(":"+port, nil))
}
