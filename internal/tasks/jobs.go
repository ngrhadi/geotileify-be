package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/hibiken/asynq"
	"github.com/ngrhadi/geotileify-be/internal/storage"
)

const TypeCleanupExpiredTiles = "tiles:cleanup_expired"

// CleanupTaskHandler deletes expired tiles from MinIO storage and the database.
type CleanupTaskHandler struct {
	DB          *sql.DB
	MinioClient *storage.Client
}

// NewCleanupExpiredTilesTask creates the task payload for the Asynq scheduler.
// No payload is needed because the handler queries the database directly.
func NewCleanupExpiredTilesTask() *asynq.Task {
	return asynq.NewTask(TypeCleanupExpiredTiles, nil)
}

// ProcessTask scans for expired tile records, deletes the associated files from MinIO,
// then removes the database entries. Errors on individual records are logged and skipped
// so one failure does not abort the entire cleanup run.
func (h *CleanupTaskHandler) ProcessTask(ctx context.Context, t *asynq.Task) error {
	rows, err := h.DB.QueryContext(ctx, "SELECT public_id, file_path FROM tiles WHERE expires_at < NOW()")
	if err != nil {
		return fmt.Errorf("cleanup: query expired tiles: %w", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var publicID, filePath string
		if err := rows.Scan(&publicID, &filePath); err != nil {
			log.Printf("cleanup: scan row: %v", err)
			continue
		}

		if err := h.MinioClient.Delete(ctx, filePath); err != nil {
			log.Printf("cleanup: delete %s from storage: %v", filePath, err)
			continue
		}

		if _, err := h.DB.ExecContext(ctx, "DELETE FROM tiles WHERE public_id = $1", publicID); err != nil {
			log.Printf("cleanup: delete %s from db: %v", publicID, err)
			continue
		}

		count++
	}

	log.Printf("cleanup: removed %d expired tile(s)", count)
	return nil
}
