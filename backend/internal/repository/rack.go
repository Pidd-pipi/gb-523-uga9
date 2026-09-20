package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
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

func placementLoadEquipmentByIDs(db *gorm.DB, ctx context.Context, ids []uint) ([]model.EquipmentLoad, error) {
	if len(ids) == 0 {
		return []model.EquipmentLoad{}, nil
	}
	var loads []model.EquipmentLoad
	if err := db.WithContext(ctx).Where("id IN ?", ids).Order("id ASC").Find(&loads).Error; err != nil {
		return nil, fmt.Errorf("load placement equipment: %w", err)
	}
	return loads, nil
}

func (r *RackRepository) ListPlacements(ctx context.Context) ([]model.RackPlacement, []model.Rack, []model.EquipmentLoad, error) {
	var placements []model.RackPlacement
	if err := r.db.WithContext(ctx).Order("rack_id ASC, id ASC").Find(&placements).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("list placements: %w", err)
	}
	racks, err := r.All(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	ids := []uint{}
	seen := map[uint]bool{}
	for _, placement := range placements {
		if !seen[placement.LoadID] {
			seen[placement.LoadID] = true
			ids = append(ids, placement.LoadID)
		}
	}
	loads, err := placementLoadEquipmentByIDs(r.db, ctx, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	return placements, racks, loads, nil
}

func (r *RackRepository) PlacementUtilization(ctx context.Context) (map[uint]dto.RackUtilization, error) {
	var rows []struct {
		RackID     uint
		PowerKW    float64
		AirflowCFM float64
		RackUnits  int
	}
	err := r.db.WithContext(ctx).Model(&model.RackPlacement{}).
		Select("rack_placements.rack_id AS rack_id, SUM(equipment_loads.power_kw) AS power_kw, SUM(equipment_loads.airflow_cfm) AS airflow_cfm, SUM(equipment_loads.rack_units) AS rack_units").
		Joins("JOIN equipment_loads ON equipment_loads.id = rack_placements.load_id").
		Group("rack_placements.rack_id").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("aggregate rack utilization: %w", err)
	}
	result := map[uint]dto.RackUtilization{}
	for _, row := range rows {
		result[row.RackID] = dto.RackUtilization{PowerKW: row.PowerKW, AirflowCFM: row.AirflowCFM, RackUnits: row.RackUnits}
	}
	return result, nil
}

// RecordMaintenanceRejection audits a rejected attempt without changing business rows.
func (r *RackRepository) RecordMaintenanceRejection(ctx context.Context, entry audit.Entry) error {
	return r.audit.Record(ctx, entry)
}

type MaintenanceInput struct {
	SourceRack      model.Rack
	Scenario        *model.LayoutScenario
	ExpectedVersion uint
	CheckVersion    bool
	Audit           audit.Entry
}

// MaintenanceInfeasible marks an in-transaction re-plan failure.
type MaintenanceInfeasible struct{ Plan planner.MaintenancePlan }

func (e *MaintenanceInfeasible) Error() string {
	return "maintenance migration infeasible under locked layout"
}

// ExecuteMaintenance transitions the rack, relocates hosted loads and creates the draft
// scenario in one transaction, re-planning against locked rows so concurrent starts win once.
func (r *RackRepository) ExecuteMaintenance(ctx context.Context, input MaintenanceInput) (planner.MaintenancePlan, error) {
	var committed planner.MaintenancePlan
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if r.db.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT id FROM racks WHERE zone_id = ? ORDER BY id FOR UPDATE", input.SourceRack.ZoneID).Error; err != nil {
				return fmt.Errorf("lock zone racks: %w", err)
			}
		}
		var source model.Rack
		if err := tx.First(&source, input.SourceRack.ID).Error; err != nil {
			return web.NotFound("rack")
		}
		if source.RackStatus != constants.RackAvailable && source.RackStatus != constants.RackReserved {
			return web.Conflict("RACK_NOT_MAINTAINABLE", fmt.Sprintf("rack %s is %s and cannot enter maintenance", source.RackCode, source.RackStatus), nil)
		}
		if input.CheckVersion && source.Version != input.ExpectedVersion {
			return web.Conflict("RACK_VERSION_CONFLICT", "rack was changed by another user before maintenance", nil)
		}
		var zones []model.ThermalZone
		if err := tx.Order("id ASC").Find(&zones).Error; err != nil {
			return fmt.Errorf("load zones: %w", err)
		}
		var racks []model.Rack
		if err := tx.Order("id ASC").Find(&racks).Error; err != nil {
			return fmt.Errorf("load racks: %w", err)
		}
		var placements []model.RackPlacement
		if err := tx.Find(&placements).Error; err != nil {
			return fmt.Errorf("load placements: %w", err)
		}
		loadIDs := make([]uint, 0, len(placements))
		for _, placement := range placements {
			loadIDs = append(loadIDs, placement.LoadID)
		}
		loads, err := placementLoadEquipmentByIDs(tx, ctx, loadIDs)
		if err != nil {
			return err
		}
		loadsByID := map[uint]model.EquipmentLoad{}
		for _, load := range loads {
			loadsByID[load.ID] = load
		}
		hosted := make([]planner.HostedEquipment, 0, len(placements))
		for _, placement := range placements {
			if load, ok := loadsByID[placement.LoadID]; ok {
				hosted = append(hosted, planner.HostedEquipment{RackID: placement.RackID, Load: load})
			}
		}
		plan := planner.PlanMaintenance(zones, racks, hosted, source.ID)
		if !plan.Feasible {
			return &MaintenanceInfeasible{Plan: plan}
		}
		committed = plan
		result := tx.Model(&model.Rack{}).
			Where("id = ? AND rack_status IN ?", source.ID, []constants.RackStatus{constants.RackAvailable, constants.RackReserved}).
			Updates(map[string]any{"rack_status": constants.RackMaintenance, "version": gorm.Expr("version + 1")})
		if result.Error != nil {
			return fmt.Errorf("set rack maintenance: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return web.Conflict("RACK_MAINTENANCE_CONFLICT", "rack maintenance was started by another request", nil)
		}
		for _, move := range plan.Moves {
			moveResult := tx.Model(&model.RackPlacement{}).Where("load_id = ? AND rack_id = ?", move.LoadID, source.ID).Update("rack_id", move.ToRackID)
			if moveResult.Error != nil {
				return fmt.Errorf("relocate placement for load %d: %w", move.LoadID, moveResult.Error)
			}
			if moveResult.RowsAffected != 1 {
				return &MaintenanceInfeasible{Plan: plan}
			}
		}
		if err := tx.Create(input.Scenario).Error; err != nil {
			return fmt.Errorf("create maintenance scenario: %w", err)
		}
		entry := input.Audit
		entry.EntityID = source.ID
		return r.audit.RecordWithDB(ctx, tx, entry)
	})
	if err != nil {
		return planner.MaintenancePlan{}, err
	}
	return committed, nil
}
