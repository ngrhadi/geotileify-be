package api

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
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
    // e.POST("/generate", func(c echo.Context) error {
    //     ctx := c.Request().Context()

    //     // cek bucket
    //     exists, err := s.MinioClient.BucketExists(ctx)
    //     if err != nil || !exists {
    //         return c.JSON(http.StatusInternalServerError, map[string]string{
    //             "error": "bucket not found",
    //         })
    //     }

    //     // Ambil file dari form-data
    //     file, err := c.FormFile("file")
    //     if err != nil {
    //         return c.JSON(http.StatusBadRequest, map[string]string{"error": "file not provided"})
    //     }

    //     // Batasi maksimal 10MB
    //     if file.Size > 10*1024*1024 {
    //         return c.JSON(http.StatusBadRequest, map[string]string{"error": "file size exceeds 10MB"})
    //     }

	// 	ext := strings.ToLower(filepath.Ext(file.Filename))

	// 	allowed := map[string]bool{
	// 		".geojson": true,
	// 		".json":    true,
	// 		".parquet": true,
	// 	}

	// 	if !allowed[ext] {
	// 		return c.JSON(http.StatusBadRequest, map[string]string{
	// 			"error": "unsupported file type",
	// 		})
	// 	}

    //     src, err := file.Open()
    //     if err != nil {
    //         return err
    //     }
    //     defer src.Close()

    //     // Simpan sementara
    //     tmpDir := "./tmp"
    //     os.MkdirAll(tmpDir, 0755)

	// 	tmpPath := filepath.Join(tmpDir, file.Filename)
	// 	outFile, err := os.Create(tmpPath)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	defer outFile.Close()

	// 	if _, err := io.Copy(outFile, src); err != nil {
	// 		return err
	// 	}

	// 	// convert parquet → geojson if needed
	// 	geojsonPath := tmpPath

	// 	if ext == ".parquet" {
	// 		geojsonPath = tmpPath + ".geojson"
	// 		if err := tile.ConvertParquetToGeoJSON(tmpPath, geojsonPath); err != nil {
	// 			return c.JSON(http.StatusInternalServerError, map[string]string{
	// 				"error": "failed to convert parquet to geojson",
	// 			})
	// 		}
	// 	}

	// 	pmtilesPath := geojsonPath + ".pmtiles"
	// 	if err := tile.GeneratePMTiles(geojsonPath, pmtilesPath); err != nil {
	// 		return c.JSON(http.StatusInternalServerError, map[string]string{
	// 			"error": "failed to generate pmtiles",
	// 		})
	// 	}

	// 	// upload ke MinIO
	// 	f, err := os.Open(pmtilesPath)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	defer f.Close()

    //     fi, _ := f.Stat()
    //     objectName := "geojson/" + file.Filename + ".pmtiles" // folder "geojson/"
    //     if err := s.MinioClient.Upload(ctx, objectName, f, fi.Size(), "application/octet-stream"); err != nil {
    //         return err
    //     }

    //     url := fmt.Sprintf("https://%s/%s/%s", s.MinioClient.Endpoint, s.MinioClient.Bucket, objectName)

    //     // Simpan metadata ke DB
    //     publicId := utils.NewULID()
    //     expiresAt := time.Now().Add(24 * time.Hour)
    //     _, err = s.DB.Exec(
    //         `INSERT INTO tiles(public_id, file_path, url, generated_at, expires_at)
    //         VALUES ($1, $2, $3, NOW(), $4)`,
    //         publicId,
    //         objectName,
    //         url,
    //         expiresAt,
    //     )
    //     if err != nil {
    //         return err
    //     }

    //     // Hapus file sementara
    //     os.Remove(tmpPath)
	// 	if geojsonPath != tmpPath {
	// 		os.Remove(geojsonPath)
	// 	}
    //     os.Remove(pmtilesPath)

    //     return c.JSON(http.StatusOK, map[string]any{
    //         "id": publicId,
    //         "download_url": fmt.Sprintf("%s/download/%s", os.Getenv("PUBLIC_BASE_URL"), publicId),
    //         "expires_at": expiresAt,
    //     })
    // })

	e.POST("/generate", func(c echo.Context) error {
		ctx := c.Request().Context()
		log.Println("=== START UPLOAD PROCESS ===")

		// STEP 1: Cek bucket
		log.Println("STEP 1: Checking bucket...")
		exists, err := s.MinioClient.BucketExists(ctx)
		if err != nil {
			log.Printf("ERROR STEP 1 - Bucket check failed: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "bucket check failed: " + err.Error(),
			})
		}
		if !exists {
			log.Println("ERROR STEP 1 - Bucket not found")
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "bucket not found",
			})
		}
		log.Println("STEP 1: Bucket OK")

		// STEP 2: Ambil file
		log.Println("STEP 2: Getting file from form...")
		file, err := c.FormFile("file")
		if err != nil {
			log.Printf("ERROR STEP 2 - File not provided: %v", err)
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "file not provided"})
		}
		log.Printf("STEP 2: File received: %s, size: %d bytes", file.Filename, file.Size)

		// STEP 3: Cek ukuran file
		log.Println("STEP 3: Checking file size...")
		if file.Size > 10*1024*1024 {
			log.Printf("ERROR STEP 3 - File too big: %d bytes", file.Size)
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "file size exceeds 10MB"})
		}
		log.Println("STEP 3: File size OK")

		// STEP 4: Cek ekstensi
		log.Println("STEP 4: Checking file extension...")
		ext := strings.ToLower(filepath.Ext(file.Filename))
		allowed := map[string]bool{
			".geojson": true,
			".json":    true,
			".parquet": true,
		}
		if !allowed[ext] {
			log.Printf("ERROR STEP 4 - Unsupported extension: %s", ext)
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": "unsupported file type",
			})
		}
		log.Printf("STEP 4: Extension OK: %s", ext)

		// STEP 5: Buka file
		log.Println("STEP 5: Opening file...")
		src, err := file.Open()
		if err != nil {
			log.Printf("ERROR STEP 5 - Cannot open file: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		defer src.Close()
		log.Println("STEP 5: File opened successfully")

		// STEP 6: Buat temp directory
		log.Println("STEP 6: Creating temp directory...")
		tmpDir := "./tmp"
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			log.Printf("ERROR STEP 6 - Cannot create temp dir: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		log.Printf("STEP 6: Temp dir ready: %s", tmpDir)

		// STEP 7: Simpan file sementara
		log.Println("STEP 7: Saving temporary file...")
		tmpPath := filepath.Join(tmpDir, file.Filename)
		outFile, err := os.Create(tmpPath)
		if err != nil {
			log.Printf("ERROR STEP 7 - Cannot create temp file: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		defer outFile.Close()

		if _, err := io.Copy(outFile, src); err != nil {
			log.Printf("ERROR STEP 7 - Cannot copy to temp file: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		log.Printf("STEP 7: Temp file saved: %s", tmpPath)

		// STEP 8: Convert parquet if needed
		geojsonPath := tmpPath
		if ext == ".parquet" {
			log.Println("STEP 8: Converting Parquet to GeoJSON...")
			geojsonPath = tmpPath + ".geojson"
			if err := tile.ConvertParquetToGeoJSON(tmpPath, geojsonPath); err != nil {
				log.Printf("ERROR STEP 8 - Parquet conversion failed: %v", err)
				return c.JSON(http.StatusInternalServerError, map[string]string{
					"error": "failed to convert parquet to geojson: " + err.Error(),
				})
			}
			log.Println("STEP 8: Parquet conversion successful")
		}

		// STEP 9: Generate PMTiles
		log.Println("STEP 9: Generating PMTiles...")
		pmtilesPath := geojsonPath + ".pmtiles"
		log.Printf("PATH: %s", os.Getenv("PATH"))
		log.Printf("Tippecanoe location: %v", exec.Command("which", "tippecanoe").Run())
		log.Printf("Tippecanoe version: %v", exec.Command("tippecanoe", "--version").Run())
		if err := tile.GeneratePMTiles(geojsonPath, pmtilesPath); err != nil {
			log.Printf("ERROR STEP 9 - PMTiles generation failed: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "failed to generate pmtiles: " + err.Error(),
			})
		}
		log.Printf("STEP 9: PMTiles generated: %s", pmtilesPath)

		// STEP 10: Upload ke MinIO
		log.Println("STEP 10: Opening PMTiles for upload...")
		f, err := os.Open(pmtilesPath)
		if err != nil {
			log.Printf("ERROR STEP 10 - Cannot open PMTiles: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil {
			log.Printf("ERROR STEP 10 - Cannot stat PMTiles: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		log.Printf("STEP 10: PMTiles size: %d bytes", fi.Size())

		objectName := "geojson/" + file.Filename + ".pmtiles"
		log.Printf("STEP 10: Uploading to MinIO: %s", objectName)

		if err := s.MinioClient.Upload(ctx, objectName, f, fi.Size(), "application/octet-stream"); err != nil {
			log.Printf("ERROR STEP 10 - MinIO upload failed: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "upload failed: " + err.Error(),
			})
		}
		log.Println("STEP 10: MinIO upload successful")

		// STEP 11: Simpan ke database
		log.Println("STEP 11: Saving to database...")
		publicId := utils.NewULID()
		expiresAt := time.Now().Add(24 * time.Hour)
		url := fmt.Sprintf("https://%s/%s/%s", s.MinioClient.Endpoint, s.MinioClient.Bucket, objectName)

		_, err = s.DB.Exec(
			`INSERT INTO tiles(public_id, file_path, url, generated_at, expires_at)
			VALUES ($1, $2, $3, NOW(), $4)`,
			publicId,
			objectName,
			url,
			expiresAt,
		)
		if err != nil {
			log.Printf("ERROR STEP 11 - Database insert failed: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "database error: " + err.Error(),
			})
		}
		log.Printf("STEP 11: Database record created: %s", publicId)

		// STEP 12: Cleanup
		log.Println("STEP 12: Cleaning up temporary files...")
		os.Remove(tmpPath)
		if geojsonPath != tmpPath {
			os.Remove(geojsonPath)
		}
		os.Remove(pmtilesPath)
		log.Println("STEP 12: Cleanup complete")

		log.Println("=== UPLOAD PROCESS COMPLETED SUCCESSFULLY ===")
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

