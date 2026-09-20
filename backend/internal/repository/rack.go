package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"gorm.io/gorm"
)

type RackRepository struct {
	db    *gorm.DB
	audit *audit.Repository
}

func NewRackRepository(db *gorm.DB, auditRepo *audit.Repository) *RackRepository {
	return &RackRepository{db: db, audit: auditRepo}
}

func (r *RackRepository) List(ctx context.Context, search, status string, zoneID uint, page, size int) ([]model.Rack, int64, error) {
	query := r.db.WithContext(ctx).Model(&model.Rack{})
	if search != "" {
		query = query.Where("LOWER(rack_code) LIKE ?", "%"+strings.ToLower(search)+"%")
	}
	if status != "" {
		query = query.Where("rack_status = ?", status)
	}
	if zoneID > 0 {
		query = query.Where("zone_id = ?", zoneID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count racks: %w", err)
	}
	var racks []model.Rack
	err := query.Preload("ThermalZone").Order("zone_id ASC, row_index ASC, column_index ASC").
		Offset((page - 1) * size).Limit(size).Find(&racks).Error
	if err != nil {
		return nil, 0, fmt.Errorf("list racks: %w", err)
	}
	return racks, total, nil
}

func (r *RackRepository) All(ctx context.Context) ([]model.Rack, error) {
	var racks []model.Rack
	err := r.db.WithContext(ctx).Preload("ThermalZone").Order("rack_code ASC").Find(&racks).Error
	if err != nil {
		return nil, fmt.Errorf("list all racks: %w", err)
	}
	return racks, nil
}

func (r *RackRepository) Get(ctx context.Context, id uint) (model.Rack, error) {
	var rack model.Rack
	if err := r.db.WithContext(ctx).Preload("ThermalZone").First(&rack, id).Error; err != nil {
		return model.Rack{}, fmt.Errorf("get rack %d: %w", id, err)
	}
	return rack, nil
}

func (r *RackRepository) Create(ctx context.Context, rack *model.Rack, entry audit.Entry) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var zoneCount int64
		if err := tx.Model(&model.ThermalZone{}).Where("id = ?", rack.ZoneID).Count(&zoneCount).Error; err != nil {
			return fmt.Errorf("validate rack zone: %w", err)
		}
		if zoneCount == 0 {
			return web.Unprocessable("ZONE_NOT_FOUND", "selected thermal zone does not exist", nil)
		}
		if err := tx.Create(rack).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return web.Conflict("RACK_CONFLICT", "rack code or zone position already exists", err)
			}
			return fmt.Errorf("create rack: %w", err)
		}
		entry.EntityID = rack.ID
		return r.audit.RecordWithDB(ctx, tx, entry)
	})
}

func (r *RackRepository) Update(ctx context.Context, rack *model.Rack, expectedVersion uint, entry audit.Entry) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.Rack{}).Where("id = ? AND version = ?", rack.ID, expectedVersion).Updates(map[string]any{
			"zone_id": rack.ZoneID, "row_index": rack.RowIndex, "column_index": rack.ColumnIndex,
			"power_limit_kw": rack.PowerLimitKW, "airflow_limit_cfm": rack.AirflowLimitCFM,
			"rack_units": rack.RackUnits, "rack_status": rack.RackStatus,
			"version": gorm.Expr("version + 1"),
		})
		if result.Error != nil {
			if errors.Is(result.Error, gorm.ErrDuplicatedKey) {
				return web.Conflict("RACK_CONFLICT", "rack position is already occupied", result.Error)
			}
			return fmt.Errorf("update rack: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return web.Conflict("RACK_VERSION_CONFLICT", "rack was changed by another user", nil)
		}
		entry.EntityID = rack.ID
		return r.audit.RecordWithDB(ctx, tx, entry)
	})
}

// MaintenanceRepository owns the atomic write that claims rack status and
// persists the migration draft, so concurrent initiations cannot both win.
type MaintenanceRepository struct {
	db    *gorm.DB
	audit *audit.Repository
}

func NewMaintenanceRepository(db *gorm.DB, auditRepo *audit.Repository) *MaintenanceRepository {
	return &MaintenanceRepository{db: db, audit: auditRepo}
}

type MaintenanceDraftInput struct {
	Name, RackAssignmentsJSON, InputSnapshotJSON, ZoneResultsJSON, ConstraintViolationsJSON string
	TotalPowerKW, PeakTempC, Score                                                          float64
	ActorID                                                                                 uint
}

// ApplyMaintenance conditionally claims the rack, freezes the migration in a
// new draft and writes both audit events in one transaction.
func (r *MaintenanceRepository) ApplyMaintenance(ctx context.Context, rack model.Rack, draft MaintenanceDraftInput, rackEntry, scenarioEntry audit.Entry) (model.LayoutScenario, error) {
	var created model.LayoutScenario
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		claim := tx.Model(&model.Rack{}).Where("id = ? AND version = ? AND rack_status IN ?", rack.ID, rack.Version,
			[]constants.RackStatus{constants.RackAvailable, constants.RackReserved}).
			Updates(map[string]any{"rack_status": constants.RackMaintenance, "version": gorm.Expr("version + 1")})
		if claim.Error != nil {
			return fmt.Errorf("claim rack for maintenance: %w", claim.Error)
		}
		if claim.RowsAffected == 0 {
			return web.Conflict("RACK_MAINTENANCE_CONFLICT", "rack maintenance was already started or the rack changed", nil)
		}
		scenario := model.LayoutScenario{
			Name: draft.Name, ScenarioStatus: constants.ScenarioDraft, RackAssignmentsJSON: draft.RackAssignmentsJSON,
			InputSnapshotJSON: draft.InputSnapshotJSON, ZoneResultsJSON: draft.ZoneResultsJSON,
			ConstraintViolationsJSON: draft.ConstraintViolationsJSON, TotalPowerKW: draft.TotalPowerKW, PeakTempC: draft.PeakTempC,
			Score: draft.Score, AlgorithmVersion: rackMaintenanceAlgorithmVersion, Version: 1, CreatedBy: draft.ActorID,
		}
		if err := tx.Create(&scenario).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return web.Conflict("SCENARIO_NAME_EXISTS", "maintenance draft name already exists", err)
			}
			return fmt.Errorf("create maintenance draft scenario: %w", err)
		}
		rackEntry.EntityID = rack.ID
		if err := r.audit.RecordWithDB(ctx, tx, rackEntry); err != nil {
			return err
		}
		scenarioEntry.EntityID = scenario.ID
		if err := r.audit.RecordWithDB(ctx, tx, scenarioEntry); err != nil {
			return err
		}
		created = scenario
		return nil
	})
	if err != nil {
		return model.LayoutScenario{}, err
	}
	return created, nil
}

// RecordRejection audits a rejected initiation; no rack/scenario row changes.
func (r *MaintenanceRepository) RecordRejection(ctx context.Context, rack model.Rack, entry audit.Entry) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entry.EntityID = rack.ID
		return r.audit.RecordWithDB(ctx, tx, entry)
	})
}

const rackMaintenanceAlgorithmVersion = "thermal-v1"
