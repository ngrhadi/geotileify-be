package main

import (
	"database/sql"
	"log"
	"os"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/ngrhadi/geotileify-be/internal/api"
	"github.com/ngrhadi/geotileify-be/internal/storage"
)

func main() {
    godotenv.Load() // Load .env

    db, err := sql.Open("postgres", os.Getenv("DB_URL"))

    if err != nil {
        log.Fatal(err)
    }

	if err := db.Ping(); err != nil {
		log.Fatal("db connection failed:", err)
	}

	log.Println("DB connected successfully")

    minioClient := storage.New(
        os.Getenv("MINIO_ENDPOINT"),
        os.Getenv("MINIO_ACCESS_KEY"),
        os.Getenv("MINIO_SECRET_KEY"),
        os.Getenv("MINIO_BUCKET"),
        true,
    )

    s := &api.Server{
        DB: db,
        MinioClient: minioClient,
    }

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Starting server on :" + port)
	e := s.Start()
	e.Logger.Fatal(e.Start(":" + port))
}
