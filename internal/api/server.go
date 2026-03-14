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

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/ngrhadi/geotileify-be/internal/storage"
	"github.com/ngrhadi/geotileify-be/internal/tile"
	"github.com/ngrhadi/geotileify-be/utils"
)

type Server struct {
    DB *sql.DB
    MinioClient *storage.Client
}

func (s *Server) Start() *echo.Echo {
    e := echo.New()
    e.Use(middleware.Recover())

	e.Use(middleware.CORS())
	// e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
    //     AllowOrigins: []string{
    //         "chrome-extension://jjcjcgoahgihmebodlkbikbahcdgmjbb", // ID Extension Anda
    //         "https://geotileify.idn-guessr.com",
    //         "http://localhost:3000",
    //         "http://localhost:5173",
    //     },
    //     AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
    //     AllowHeaders: []string{"Content-Type", "Authorization"},
    //     AllowCredentials: true,
    // }))

    e.Use(middleware.RequestLogger())

	config := middleware.RateLimiterConfig{
        Skipper: middleware.DefaultSkipper,
        Store: middleware.NewRateLimiterMemoryStoreWithConfig(
            middleware.RateLimiterMemoryStoreConfig{
                Rate:      10,              // 10 request per detik
                Burst:     30,              // burst 30 request
                ExpiresIn: 3 * time.Minute,
            },
        ),
        IdentifierExtractor: func(c echo.Context) (string, error) {
            return c.RealIP(), nil
        },
        ErrorHandler: func(c echo.Context, err error) error {
            return c.JSON(429, map[string]string{
                "error": "Too many requests",
            })
        },
        DenyHandler: func(c echo.Context, identifier string, err error) error {
            return c.JSON(429, map[string]string{
                "error": "Rate limit exceeded. Please try again later.",
            })
        },
    }

    e.Use(middleware.RateLimiterWithConfig(config))

	e.Use(middleware.BodyLimit("50M"))

    // Upload & generate endpoint
    e.POST("/generate", func(c echo.Context) error {
        ctx := c.Request().Context()

        // cek bucket
        exists, err := s.MinioClient.BucketExists(ctx)
        if err != nil || !exists {
            return c.JSON(http.StatusInternalServerError, map[string]string{
                "error": "bucket not found",
            })
        }

        // Ambil file dari form-data
        file, err := c.FormFile("file")
        if err != nil {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "file not provided"})
        }

        // Batasi maksimal 10MB
        if file.Size > 10*1024*1024 {
            return c.JSON(http.StatusBadRequest, map[string]string{"error": "file size exceeds 10MB"})
        }

		ext := strings.ToLower(filepath.Ext(file.Filename))

		allowed := map[string]bool{
			".geojson": true,
			".json":    true,
			".parquet": true,
		}

		if !allowed[ext] {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": "unsupported file type",
			})
		}

        src, err := file.Open()
        if err != nil {
            return err
        }
        defer src.Close()

        // Simpan sementara
        tmpDir := "./tmp"
        os.MkdirAll(tmpDir, 0755)

		tmpPath := filepath.Join(tmpDir, file.Filename)
		outFile, err := os.Create(tmpPath)
		if err != nil {
			return err
		}
		defer outFile.Close()

		if _, err := io.Copy(outFile, src); err != nil {
			return err
		}

		// convert parquet → geojson if needed
		geojsonPath := tmpPath

		if ext == ".parquet" {
			geojsonPath = tmpPath + ".geojson"
			if err := tile.ConvertParquetToGeoJSON(tmpPath, geojsonPath); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]string{
					"error": "failed to convert parquet to geojson",
				})
			}
		}

		pmtilesPath := geojsonPath + ".pmtiles"
		if err := tile.GeneratePMTiles(geojsonPath, pmtilesPath); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "failed to generate pmtiles",
			})
		}

		// upload ke MinIO
		f, err := os.Open(pmtilesPath)
		if err != nil {
			return err
		}
		defer f.Close()

        fi, _ := f.Stat()
        objectName := "geojson/" + file.Filename + ".pmtiles" // folder "geojson/"
        if err := s.MinioClient.Upload(ctx, objectName, f, fi.Size(), "application/octet-stream"); err != nil {
            return err
        }

        url := fmt.Sprintf("https://%s/%s/%s", s.MinioClient.Endpoint, s.MinioClient.Bucket, objectName)

        // Simpan metadata ke DB
        publicId := utils.NewULID()
        expiresAt := time.Now().Add(24 * time.Hour)
        _, err = s.DB.Exec(
            `INSERT INTO tiles(public_id, file_path, url, generated_at, expires_at)
            VALUES ($1, $2, $3, NOW(), $4)`,
            publicId,
            objectName,
            url,
            expiresAt,
        )
        if err != nil {
            return err
        }

        // Hapus file sementara
        os.Remove(tmpPath)
		if geojsonPath != tmpPath {
			os.Remove(geojsonPath)
		}
        os.Remove(pmtilesPath)

        return c.JSON(http.StatusOK, map[string]any{
            "id": publicId,
            "download_url": fmt.Sprintf("%s/download/%s", os.Getenv("PUBLIC_BASE_URL"), publicId),
            "expires_at": expiresAt,
        })
    })

	e.GET("/download/:id", func(c echo.Context) error {
		id := c.Param("id")
		var objectName, url string
		var expiresAt time.Time

		// Ambil metadata dari DB
		err := s.DB.QueryRow(
			"SELECT file_path, url, expires_at FROM tiles WHERE public_id=$1",
			id,
		).Scan(&objectName, &url, &expiresAt)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "tile not found"})
		}

		if time.Now().After(expiresAt) {
			// Hapus file dari bucket
			err := s.MinioClient.Delete(c.Request().Context(), objectName)
			if err != nil {
				log.Println("failed to delete expired object:", err)
			}
			return c.JSON(http.StatusForbidden, map[string]string{"error": "link expired"})
		}

		// Gunakan context dari request, jangan ctx global
		presignedURL, err := s.MinioClient.PresignDownload(c.Request().Context(), objectName, 5*time.Minute)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to generate download link: %v", err),
			})
		}

		// Redirect ke presigned URL
		return c.Redirect(http.StatusFound, presignedURL)
	})

	e.GET("/tiles/:id", func(c echo.Context) error {
		id := c.Param("id")

		// Ambil metadata dari DB
		var objectName string
		var expiresAt time.Time

		err := s.DB.QueryRow(
			"SELECT file_path, expires_at FROM tiles WHERE public_id=$1",
			id,
		).Scan(&objectName, &expiresAt)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "tile not found"})
		}

		// Cek expired
		if time.Now().After(expiresAt) {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "link expired"})
		}

		// Gunakan MinIO client untuk mendapatkan object dengan range request
		// Buat presigned URL untuk GET dengan durasi pendek
		presignedURL, err := s.MinioClient.PresignGetObject(c.Request().Context(), objectName, 5*time.Minute)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": fmt.Sprintf("failed to generate access link: %v", err),
			})
		}

		// Proxy request ke MinIO dengan range header yang sama
		// atau redirect dengan status 302
		return c.Redirect(http.StatusFound, presignedURL)
	})


    return e
}

