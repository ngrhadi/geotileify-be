package api

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/ngrhadi/geotileify-be/internal/storage"
	"github.com/ngrhadi/geotileify-be/internal/tasks"
	"github.com/ngrhadi/geotileify-be/internal/tile"
	"github.com/ngrhadi/geotileify-be/utils"
)

// Server holds the shared dependencies injected at startup.
type Server struct {
	DB             *sql.DB
	MinioClient    *storage.Client
	asynqClient    *asynq.Client
	asynqScheduler *asynq.Scheduler
}

// Start registers middleware and routes, starts background workers,
// and returns the configured Echo instance ready to serve.
func (s *Server) Start() *echo.Echo {
	e := echo.New()
	e.Use(middleware.Recover())

	s.startBackgroundWorkers()
	s.registerMiddleware(e)
	s.registerRoutes(e)

	return e
}

// startBackgroundWorkers initialises the Asynq scheduler and worker pool.
// The cleanup job runs every 15 minutes to remove expired tiles.
func (s *Server) startBackgroundWorkers() {
	redisAddr := strings.TrimPrefix(os.Getenv("REDIS_URL"), "redis://")

	s.asynqClient = asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})
	s.asynqScheduler = asynq.NewScheduler(asynq.RedisClientOpt{Addr: redisAddr}, nil)

	cleanupHandler := &tasks.CleanupTaskHandler{
		DB:          s.DB,
		MinioClient: s.MinioClient,
	}

	task := tasks.NewCleanupExpiredTilesTask()
	entryID, err := s.asynqScheduler.Register("*/15 * * * *", task)
	if err != nil {
		log.Fatalf("could not register cleanup scheduler: %v", err)
	}
	log.Printf("cleanup task scheduled (entry: %s)", entryID)

	go func() {
		mux := asynq.NewServeMux()
		mux.HandleFunc(tasks.TypeCleanupExpiredTiles, cleanupHandler.ProcessTask)
		worker := asynq.NewServer(
			asynq.RedisClientOpt{Addr: redisAddr},
			asynq.Config{Concurrency: 10},
		)
		if err := worker.Run(mux); err != nil {
			log.Fatalf("asynq worker stopped: %v", err)
		}
	}()

	go func() {
		if err := s.asynqScheduler.Run(); err != nil {
			log.Fatalf("asynq scheduler stopped: %v", err)
		}
	}()
}

func (s *Server) registerMiddleware(e *echo.Echo) {
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{
			"chrome-extension://ljoijfpcddgiadohecobajkkbbemmaio",
			"chrome-extension://jjcjcgoahgihmebodlkbikbahcdgmjbb",
			"https://geotileify.idn-guessr.com",
		},
		AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders:     []string{"Content-Type", "Authorization"},
		AllowCredentials: true,
	}))

	e.Use(middleware.RequestLogger())

	e.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Skipper: middleware.DefaultSkipper,
		Store: middleware.NewRateLimiterMemoryStoreWithConfig(
			middleware.RateLimiterMemoryStoreConfig{
				Rate:      10,
				Burst:     30,
				ExpiresIn: 3 * time.Minute,
			},
		),
		IdentifierExtractor: func(c echo.Context) (string, error) {
			return c.RealIP(), nil
		},
		ErrorHandler: func(c echo.Context, err error) error {
			return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "Too many requests"})
		},
		DenyHandler: func(c echo.Context, identifier string, err error) error {
			return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "Rate limit exceeded. Please try again later."})
		},
	}))

	e.Use(middleware.BodyLimit("50M"))
}

func (s *Server) registerRoutes(e *echo.Echo) {
	e.GET("/", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/health")
	})
	e.GET("/health", s.handleHealth)
	e.POST("/generate", s.handleGenerate)
	e.GET("/download/:id", s.handleDownload)
	e.GET("/tiles/:id", s.handleStreamTile)
}

// handleHealth returns the current service status and uptime information.
func (s *Server) handleHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "geotileify",
		"message": "Service is running",
	})
}

// handleGenerate accepts a GeoJSON, JSON, or Parquet file upload, converts it to PMTiles
// using tippecanoe, stores the result in MinIO, and records metadata with a 24-hour TTL.
func (s *Server) handleGenerate(c echo.Context) error {
	ctx := c.Request().Context()

	exists, err := s.MinioClient.BucketExists(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "bucket check failed: " + err.Error()})
	}
	if !exists {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "storage bucket not found"})
	}

	file, err := c.FormFile("file")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "file not provided"})
	}

	if file.Size > 50*1024*1024 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "file size exceeds 50MB"})
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	allowed := map[string]bool{
		".geojson":  true, // GeoJSON
		".json":     true, // GeoJSON (alternate extension)
		".geojsons": true, // GeoJSON Sequences — RFC 8142
		".geojsonl": true, // GeoJSON Lines — common in GDAL/Tippecanoe
		".fgb":      true, // FlatGeobuf
		".parquet":  true, // GeoParquet — converted via DuckDB
		".zip":      true, // Shapefile bundle (.shp + .dbf + .shx)
	}
	if !allowed[ext] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported file type"})
	}

	src, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer src.Close()

	tmpPath := filepath.Join(os.TempDir(), file.Filename)
	outFile, err := os.Create(tmpPath)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if _, err := io.Copy(outFile, src); err != nil {
		outFile.Close()
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	// Close explicitly so the external tippecanoe process can read the file.
	outFile.Close()

	geojsonPath := tmpPath
	switch ext {
	case ".zip":
		geojsonPath = tmpPath + ".geojson"
		if err := tile.ConvertShapefileZipToGeoJSON(tmpPath, geojsonPath); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "shapefile conversion failed: " + err.Error()})
		}
	case ".parquet":
		geojsonPath = tmpPath + ".geojson"
		if err := tile.ConvertParquetToGeoJSON(tmpPath, geojsonPath); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "parquet conversion failed: " + err.Error()})
		}
	}

	pmtilesPath := geojsonPath + ".pmtiles"
	if err := tile.GeneratePMTiles(geojsonPath, pmtilesPath); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "tile generation failed: " + err.Error()})
	}

	f, err := os.Open(pmtilesPath)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	objectName := "geojson/" + file.Filename + ".pmtiles"
	if err := s.MinioClient.Upload(ctx, objectName, f, fi.Size(), "application/octet-stream"); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "upload failed: " + err.Error()})
	}

	publicID := utils.NewULID()
	expiresAt := time.Now().Add(24 * time.Hour)
	tileURL := fmt.Sprintf("https://%s/%s/%s", s.MinioClient.Endpoint, s.MinioClient.Bucket, objectName)

	_, err = s.DB.Exec(
		`INSERT INTO tiles (public_id, file_path, url, generated_at, expires_at) VALUES ($1, $2, $3, NOW(), $4)`,
		publicID, objectName, tileURL, expiresAt,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "database error: " + err.Error()})
	}

	// Best-effort cleanup of temporary files.
	os.Remove(tmpPath)
	if geojsonPath != tmpPath {
		os.Remove(geojsonPath)
	}
	os.Remove(pmtilesPath)

	log.Printf("tile generated: %s (expires: %s)", publicID, expiresAt.Format(time.RFC3339))

	return c.JSON(http.StatusOK, map[string]any{
		"id":           publicID,
		"download_url": fmt.Sprintf("%s/download/%s", os.Getenv("PUBLIC_BASE_URL"), publicID),
		"expires_at":   expiresAt,
	})
}

// handleDownload redirects to a short-lived presigned MinIO URL that triggers a file download.
// Expired tiles are cleaned up inline and return 403.
func (s *Server) handleDownload(c echo.Context) error {
	id := c.Param("id")
	var objectName, url string
	var expiresAt time.Time

	err := s.DB.QueryRow(
		"SELECT file_path, url, expires_at FROM tiles WHERE public_id=$1", id,
	).Scan(&objectName, &url, &expiresAt)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "tile not found"})
	}

	if time.Now().After(expiresAt) {
		if err := s.MinioClient.Delete(c.Request().Context(), objectName); err != nil {
			log.Println("failed to delete expired tile:", err)
		}
		return c.JSON(http.StatusForbidden, map[string]string{"error": "link expired"})
	}

	presignedURL, err := s.MinioClient.PresignDownload(c.Request().Context(), objectName, 5*time.Minute)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to generate download link: %v", err),
		})
	}

	return c.Redirect(http.StatusFound, presignedURL)
}

// handleStreamTile redirects to a presigned MinIO URL that supports HTTP range requests,
// enabling PMTiles clients to fetch only the byte ranges they need.
func (s *Server) handleStreamTile(c echo.Context) error {
	id := c.Param("id")
	var objectName string
	var expiresAt time.Time

	err := s.DB.QueryRow(
		"SELECT file_path, expires_at FROM tiles WHERE public_id=$1", id,
	).Scan(&objectName, &expiresAt)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "tile not found"})
	}

	if time.Now().After(expiresAt) {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "link expired"})
	}

	presignedURL, err := s.MinioClient.PresignGetObject(c.Request().Context(), objectName, 5*time.Minute)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": fmt.Sprintf("failed to generate access link: %v", err),
		})
	}

	return c.Redirect(http.StatusFound, presignedURL)
}
