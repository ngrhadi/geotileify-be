package tasks

import (
	"context"
	"database/sql"
	"log"

	"github.com/hibiken/asynq"
	"github.com/ngrhadi/geotileify-be/internal/storage"
)

const (
    // TypeCleanupExpiredTiles adalah nama unik untuk task ini
    TypeCleanupExpiredTiles = "tiles:cleanup_expired"
)

// CleanupTaskHandler menyimpan dependency yang dibutuhkan untuk task pembersihan
type CleanupTaskHandler struct {
    DB          *sql.DB
    MinioClient *storage.Client
}

// NewCleanupExpiredTilesTask membuat task baru untuk dikirim ke antrian
func NewCleanupExpiredTilesTask() (*asynq.Task, error) {
    // Task ini tidak membutuhkan payload (data kosong), karena akan scan DB
    return asynq.NewTask(TypeCleanupExpiredTiles, nil), nil
}

// ProcessTask adalah logika yang dijalankan worker saat task dieksekusi
func (h *CleanupTaskHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
    log.Println("INFO: Memulai job pembersihan file expired...")

    // 1. Query data yang sudah expired
    // Kita ambil public_id dan file_path untuk proses cleanup
    rows, err := h.DB.Query("SELECT public_id, file_path FROM tiles WHERE expires_at < NOW()")
    if err != nil {
        log.Printf("ERROR: Gagal query database: %v", err)
        return err
    }
    defer rows.Close()

    var count int

    for rows.Next() {
        var publicID string
        var filePath string

        if err := rows.Scan(&publicID, &filePath); err != nil {
            log.Printf("ERROR: Gagal scan row: %v", err)
            continue
        }

        // 2. Hapus file dari Minio Storage
        // Menggunakan filePath (objectName) yang didapat dari DB
        if err := h.MinioClient.Delete(ctx, filePath); err != nil {
            log.Printf("ERROR: Gagal hapus file %s dari Minio: %v", filePath, err)
            // Lanjutkan ke record berikutnya meskipun gagal hapus file, atau bisa return error sesuai kebutuhan
            continue
        }

        // 3. Hapus record dari Database
        // Setelah file fisik dihapus, hapus juga metadata di DB
        _, err := h.DB.Exec("DELETE FROM tiles WHERE public_id = $1", publicID)
        if err != nil {
            log.Printf("ERROR: Gagal hapus record DB %s: %v", publicID, err)
            continue
        }

        log.Printf("SUCCESS: Berhasil membersihkan tile ID: %s", publicID)
        count++
    }

    log.Printf("INFO: Job pembersihan selesai. Total %d file dihapus.", count)
    return nil
}
