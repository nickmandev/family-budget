package postgres

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"family-budget/internal/ports"
)

type workerHeartbeatRepository struct {
	db *gorm.DB
}

func (r *workerHeartbeatRepository) Upsert(ctx context.Context, record ports.WorkerHeartbeatRecord) error {
	row := WorkerHeartbeatModel{
		WorkerName:       record.WorkerName,
		StartedAt:        record.StartedAt.UTC(),
		LastTickAt:       record.LastTickAt.UTC(),
		LastErrorAt:      utcTimePointer(record.LastErrorAt),
		LastErrorSummary: record.LastErrorSummary,
	}
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "worker_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"started_at", "last_tick_at", "last_error_at", "last_error_summary"}),
	}).Create(&row).Error
	return toDomainErr(err)
}

func (r *workerHeartbeatRepository) Get(ctx context.Context, workerName string) (ports.WorkerHeartbeatRecord, error) {
	var row WorkerHeartbeatModel
	if err := r.db.WithContext(ctx).Where("worker_name = ?", workerName).Take(&row).Error; err != nil {
		return ports.WorkerHeartbeatRecord{}, toDomainErr(err)
	}
	return ports.WorkerHeartbeatRecord{
		WorkerName:       row.WorkerName,
		StartedAt:        row.StartedAt.UTC(),
		LastTickAt:       row.LastTickAt.UTC(),
		LastErrorAt:      utcTimePointer(row.LastErrorAt),
		LastErrorSummary: row.LastErrorSummary,
	}, nil
}
