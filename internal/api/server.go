package api

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/ngrhadi/geotileify-be/internal/storage"
	"github.com/ngrhadi/geotileify-be/internal/tasks"
	"github.com/ngrhadi/geotileify-be/internal/tile"
	"github.com/ngrhadi/geotileify-be/utils"
	"github.com/ngrhadi/geotileify-be/web"
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
		AllowHeaders:     []string{"Content-Type", "Authorization", "Range"},
		ExposeHeaders:    []string{"Content-Range", "Content-Length", "Accept-Ranges", "Content-Type", "ETag", "Cache-Control", "Expires"},
		AllowCredentials: true,
	}))

	e.Use(middleware.RequestLogger())

	e.Use(middleware.RateLimiterWithConfig(middleware.RateLimiterConfig{
		Skipper: func(c echo.Context) bool {
			// Landing page, static assets, health, dan tile streaming tidak
			// kena rate-limit agar preview map/CSS/JS tidak dapat 429.
			p := c.Path()
			if p == "/" || p == "/health" || p == "/privacy" ||
				strings.HasPrefix(p, "/static") || strings.HasPrefix(p, "/tiles/") {
				return true
			}
			// c.Path() kosong saat 404 — fallback ke Request URL path.
			rp := c.Request().URL.Path
			if rp == "/" || rp == "/health" || rp == "/privacy" ||
				strings.HasPrefix(rp, "/static/") || strings.HasPrefix(rp, "/tiles/") {
				return true
			}
			return false
		},
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
	// Landing page and legal pages from web/static (embedded at build time).
	e.GET("/", s.handleIndex)
	e.GET("/privacy", s.handlePrivacy)
	// Static assets (css/js): correct Content-Type + long cache (immutable files).
	e.GET("/static/*", s.handleStatic)

	e.GET("/health", s.handleHealth)
	e.POST("/generate", s.handleGenerate)
	e.GET("/download/:id", s.handleDownload)
	e.GET("/tiles/:id", s.handleStreamTile)
	e.GET("/tiles/:id/proxy", s.handleProxyTile)
}

// staticFS returns the embedded web/static subtree as fs.FS.
func staticFS() fs.FS {
	sub, err := fs.Sub(web.Static, "static")
	if err != nil {
		// embed.FS always contains "static" — this is unreachable, panic keeps
		// the failure loud at startup instead of serving 500s.
		panic("web/static not embedded: " + err.Error())
	}
	return sub
}

// handleIndex serves the landing page HTML.
// Uses text/html; charset=utf-8 explicitly and no-store so deploys baru
// langsung terlihat (HTML tidak di-cache, asset css/js yang di-cache).
func (s *Server) handleIndex(c echo.Context) error {
	return s.serveEmbeddedPage(c, "index.html")
}

// handlePrivacy serves the embedded privacy policy page.
func (s *Server) handlePrivacy(c echo.Context) error {
	return s.serveEmbeddedPage(c, "privacy.html")
}

// serveEmbeddedPage serves a standalone HTML page with no-store caching.
func (s *Server) serveEmbeddedPage(c echo.Context, name string) error {
	html, err := fs.ReadFile(staticFS(), name)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "page not found"})
	}
	c.Response().Header().Set(echo.HeaderContentType, "text/html; charset=utf-8")
	c.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	return c.Blob(http.StatusOK, "text/html; charset=utf-8", html)
}

// handleStatic serves embedded files under web/static/* (css, js, images).
// Path traversal diblokir via path.Clean + penolakan "..".
// Content-Type diambil dari ekstensi (mime.TypeByExtension) dengan fallback aman,
// dan Cache-Control agresif untuk css/js agar browser cache dengan benar.
func (s *Server) handleStatic(c echo.Context) error {
	raw := c.Param("*")                    // e.g. "css/style.css"
	clean := filepath.Clean("/" + raw)[1:] // strip leading slash
	if clean == "." || clean == "" || strings.Contains(clean, "..") {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid asset path"})
	}

	data, err := fs.ReadFile(staticFS(), clean)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "asset not found"})
	}

	ct := mime.TypeByExtension(filepath.Ext(clean))
	if ct == "" {
		// Fallback aman: jangan biarkan browser menebak (X-Content-Type-Options: nosniff).
		ct = "application/octet-stream"
	}
	// Pastikan charset untuk text-based agar tidak mojibake.
	if strings.HasPrefix(ct, "text/") && !strings.Contains(ct, "charset") {
		ct += "; charset=utf-8"
	}

	h := c.Response().Header()
	h.Set(echo.HeaderContentType, ct)
	h.Set("X-Content-Type-Options", "nosniff")
	// css/js boleh di-cache lama (nama file versioned/manual invalidate);
	// html diserve terpisah tanpa cache (lihat handleIndex).
	if strings.HasSuffix(clean, ".css") || strings.HasSuffix(clean, ".js") {
		h.Set(echo.HeaderCacheControl, "public, max-age=86400")
	} else {
		h.Set(echo.HeaderCacheControl, "public, max-age=3600")
	}

	return c.Blob(http.StatusOK, ct, data)
}

// handleHealth returns the current service status and uptime information.
func (s *Server) handleHealth(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "geotileify",
		"message": "Service is running",
	})
}

// handleGenerate accepts a supported geospatial upload, normalises it to
// GeoParquet with DuckDB Spatial, and generates PMTiles with tylertoo.
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
		".geojsonl": true, // GeoJSON Lines
		".fgb":      true, // FlatGeobuf
		".parquet":  true, // GeoParquet
		".zip":      true, // Shapefile bundle (.shp + .dbf + .shx)
	}
	if !allowed[ext] {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "unsupported file type"})
	}

	minZoom, err := parseZoom(c.FormValue("min_zoom"), tile.DefaultMinZoom)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid min_zoom: " + err.Error()})
	}
	maxZoom, err := parseZoom(c.FormValue("max_zoom"), tile.DefaultMaxZoom)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid max_zoom: " + err.Error()})
	}
	simplifyFactor, err := parseSimplifyFactor(c.FormValue("simplify_factor"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid simplify_factor: " + err.Error()})
	}
	excludeAllProperties, err := parseBool(c.FormValue("exclude_all_properties"), false)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid exclude_all_properties: " + err.Error()})
	}
	includeProperties := parsePropertyList(c.FormValue("include_properties"))
	if len(includeProperties) > 0 {
		// --include-property overrides --exclude-all-properties.
		excludeAllProperties = false
	}
	opts := tile.Options{
		MinZoom:              minZoom,
		MaxZoom:              maxZoom,
		LayerName:            tile.LayerName(file.Filename),
		SimplifyFactor:       simplifyFactor,
		ExcludeAllProperties: excludeAllProperties,
		IncludeProperties:    includeProperties,
		Metadata: tile.MetadataFields{
			Name:        strings.TrimSpace(c.FormValue("name")),
			Description: strings.TrimSpace(c.FormValue("description")),
			Attribution: strings.TrimSpace(c.FormValue("attribution")),
		},
	}
	if err := opts.Validate(); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid tiling options: " + err.Error()})
	}

	// One isolated work directory per request; defer removes every temp file.
	workDir, err := os.MkdirTemp("", "geotileify-")
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "temp directory failed: " + err.Error()})
	}
	defer os.RemoveAll(workDir)

	uploadPath := filepath.Join(workDir, "upload"+ext)
	src, err := file.Open()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	defer src.Close()

	dst, err := os.Create(uploadPath)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	if err := dst.Close(); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}

	parquetPath := filepath.Join(workDir, "input.parquet")
	var tilerInput string
	switch ext {
	case ".zip":
		if err := tile.ConvertShapefileZipToGeoParquet(uploadPath, parquetPath); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "shapefile conversion failed: " + err.Error()})
		}
		tilerInput = parquetPath
	case ".parquet":
		prepared, err := tile.PrepareGeoParquet(uploadPath, parquetPath)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "parquet preparation failed: " + err.Error()})
		}
		tilerInput = prepared
	default:
		if err := tile.ConvertToGeoParquet(uploadPath, parquetPath); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "geoparquet conversion failed: " + err.Error()})
		}
		tilerInput = parquetPath
	}

	pmtilesPath := filepath.Join(workDir, "output.pmtiles")
	if err := tile.GeneratePMTiles(tilerInput, pmtilesPath, opts); err != nil {
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

	publicID := utils.NewULID()
	objectName := "tiles/" + publicID + ".pmtiles"
	if err := s.MinioClient.Upload(ctx, objectName, f, fi.Size(), "application/vnd.pmtiles"); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "upload failed: " + err.Error()})
	}

	expiresAt := time.Now().Add(24 * time.Hour)
	tileURL := fmt.Sprintf("https://%s/%s/%s", s.MinioClient.Endpoint, s.MinioClient.Bucket, objectName)

	_, err = s.DB.Exec(
		`INSERT INTO tiles (public_id, file_path, url, generated_at, expires_at) VALUES ($1, $2, $3, NOW(), $4)`,
		publicID, objectName, tileURL, expiresAt,
	)
	if err != nil {
		// Do not leave an orphan object when metadata persistence fails.
		_ = s.MinioClient.Delete(ctx, objectName)
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "database error: " + err.Error()})
	}

	log.Printf("tile generated: %s (layer: %s, zoom: %d-%d, expires: %s)", publicID, opts.LayerName, opts.MinZoom, opts.MaxZoom, expiresAt.Format(time.RFC3339))

	return c.JSON(http.StatusOK, map[string]any{
		"id":           publicID,
		"download_url": fmt.Sprintf("%s/download/%s", os.Getenv("PUBLIC_BASE_URL"), publicID),
		"expires_at":   expiresAt,
		"min_zoom":     opts.MinZoom,
		"max_zoom":     opts.MaxZoom,
	})
}

func parseZoom(raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("must be an integer")
	}
	return n, nil
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

// handleProxyTile streams PMTiles bytes through the API with HTTP range
// support. It exists so the extension map preview can read a generated archive
// without relying on MinIO CORS headers. /tiles/:id keeps the redirect
// behaviour for external PMTiles clients.
func (s *Server) handleProxyTile(c echo.Context) error {
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

	ctx := c.Request().Context()
	info, err := s.MinioClient.StatObject(ctx, objectName)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "tile object not found"})
	}
	if info.Size <= 0 {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "tile object is empty"})
	}

	start, end, partial, err := parseRangeHeader(c.Request().Header.Get("Range"), info.Size)
	if err != nil {
		if errors.Is(err, errRangeNotSatisfiable) {
			c.Response().Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size))
		}
		return c.JSON(http.StatusRequestedRangeNotSatisfiable, map[string]string{"error": "invalid range"})
	}

	obj, err := s.MinioClient.OpenObjectRange(ctx, objectName, start, end)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to read tile object"})
	}
	defer obj.Close()

	contentType := info.ContentType
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = "application/vnd.pmtiles"
	}

	h := c.Response().Header()
	h.Set("Content-Type", contentType)
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	if info.ETag != "" {
		h.Set("ETag", info.ETag)
	}
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, info.Size))
		c.Response().WriteHeader(http.StatusPartialContent)
	} else {
		c.Response().WriteHeader(http.StatusOK)
	}

	if _, err := io.Copy(c.Response(), obj); err != nil {
		log.Printf("proxy tile %s: stream error: %v", id, err)
	}
	return nil
}

var errRangeNotSatisfiable = errors.New("range not satisfiable")

// parseRangeHeader parses a single HTTP byte range. start/end are inclusive.
func parseRangeHeader(raw string, size int64) (start, end int64, partial bool, err error) {
	if size <= 0 {
		return 0, 0, false, errRangeNotSatisfiable
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, size - 1, false, nil
	}
	if !strings.HasPrefix(raw, "bytes=") {
		return 0, 0, false, fmt.Errorf("unsupported range unit")
	}

	spec := strings.TrimSpace(strings.TrimPrefix(raw, "bytes="))
	if spec == "" || strings.Contains(spec, ",") {
		return 0, 0, false, errRangeNotSatisfiable
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, errRangeNotSatisfiable
	}

	if parts[0] == "" {
		// Suffix range: bytes=-N
		n, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || n <= 0 {
			return 0, 0, false, errRangeNotSatisfiable
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, nil
	}

	start, parseErr := strconv.ParseInt(parts[0], 10, 64)
	if parseErr != nil || start < 0 || start >= size {
		return 0, 0, false, errRangeNotSatisfiable
	}
	if parts[1] == "" {
		return start, size - 1, true, nil
	}

	end, parseErr = strconv.ParseInt(parts[1], 10, 64)
	if parseErr != nil || end < start {
		return 0, 0, false, errRangeNotSatisfiable
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true, nil
}

func parseSimplifyFactor(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1.0, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 || value > 10 {
		return 0, fmt.Errorf("must be a number greater than 0 and at most 10")
	}
	return value, nil
}

func parseBool(raw string, fallback bool) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("must be true or false")
	}
	return value, nil
}

func parsePropertyList(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		property := strings.TrimSpace(part)
		if property == "" || seen[property] {
			continue
		}
		seen[property] = true
		out = append(out, property)
	}
	return out
}
