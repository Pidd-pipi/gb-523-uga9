package service

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var rackFixtureSeq int64

func rackMaintenanceFixture(t *testing.T) (*RackService, *gorm.DB) {
	t.Helper()
	rackFixtureSeq++
	dsn := filepath.Join(t.TempDir(), fmt.Sprintf("maint-%d.db?_journal_mode=WAL&_busy_timeout=10000", rackFixtureSeq))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Single connection serializes SQLite write transactions; the conditional status
	// update still guarantees exactly one success. Postgres uses FOR UPDATE row locks.
	if sqlDB, err := db.DB(); err != nil {
		t.Fatalf("sqlite handle: %v", err)
	} else {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(&model.ThermalZone{}, &model.Rack{}, &model.EquipmentLoad{}, &model.RackPlacement{}, &model.LayoutScenario{}, &audit.Event{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	zones := []model.ThermalZone{
		{ID: 1, ZoneCode: "TZ-A", Name: "A", CoolingCapacityKW: 200, SupplyTempC: 18, MaxReturnTempC: 32, ZoneStatus: "active"},
		{ID: 2, ZoneCode: "TZ-B", Name: "B", CoolingCapacityKW: 200, SupplyTempC: 18, MaxReturnTempC: 32, ZoneStatus: "active"},
	}
	if err := db.Create(&zones).Error; err != nil {
		t.Fatalf("seed zones: %v", err)
	}
	racks := []model.Rack{
		{ID: 10, ZoneID: 1, RackCode: "A-01", RowIndex: 1, ColumnIndex: 1, PowerLimitKW: 30, AirflowLimitCFM: 7200, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 11, ZoneID: 1, RackCode: "A-02", RowIndex: 1, ColumnIndex: 2, PowerLimitKW: 30, AirflowLimitCFM: 7200, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 20, ZoneID: 2, RackCode: "B-01", RowIndex: 2, ColumnIndex: 1, PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 21, ZoneID: 2, RackCode: "B-02", RowIndex: 2, ColumnIndex: 2, PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackReserved, Version: 1},
	}
	if err := db.Create(&racks).Error; err != nil {
		t.Fatalf("seed racks: %v", err)
	}
	loads := []model.EquipmentLoad{
		{ID: 1, Name: "node-A", PowerKW: 18, HeatKW: 17, AirflowCFM: 5000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"},
		{ID: 2, Name: "node-B", PowerKW: 12, HeatKW: 11, AirflowCFM: 3000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"},
		{ID: 3, Name: "node-C", PowerKW: 15, HeatKW: 14, AirflowCFM: 4000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"},
	}
	if err := db.Create(&loads).Error; err != nil {
		t.Fatalf("seed loads: %v", err)
	}
	placements := []model.RackPlacement{
		{RackID: 10, LoadID: 1, Source: "seed"},
		{RackID: 20, LoadID: 2, Source: "seed"},
		{RackID: 21, LoadID: 3, Source: "seed"},
	}
	if err := db.Create(&placements).Error; err != nil {
		t.Fatalf("seed placements: %v", err)
	}

	auditRepo := audit.NewRepository(db)
	rackRepo := repository.NewRackRepository(db, auditRepo)
	zoneRepo := repository.NewThermalZoneRepository(db, auditRepo)
	return NewRackService(rackRepo, zoneRepo), db
}

func rackTestActor() audit.Entry {
	return audit.Entry{RequestID: "req-test", ActorID: 7, ActorUsername: "planner"}
}

func TestStartMaintenanceSuccess(t *testing.T) {
	service, db := rackMaintenanceFixture(t)
	result, err := service.StartMaintenance(context.Background(), 10, dto.StartMaintenanceRequest{}, rackTestActor())
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if result.RackStatus != "maintenance" || result.MovedLoads != 1 {
		t.Fatalf("unexpected result %+v", result)
	}
	if len(result.Moves) != 1 || result.Moves[0].ToRackCode != "A-02" {
		t.Fatalf("expected move to A-02, got %+v", result.Moves)
	}
	var rack model.Rack
	if err := db.First(&rack, 10).Error; err != nil || rack.RackStatus != constants.RackMaintenance || rack.Version != 2 {
		t.Fatalf("rack not persisted as maintenance: %+v err=%v", rack, err)
	}
	var placement model.RackPlacement
	if err := db.Where("load_id = ?", 1).First(&placement).Error; err != nil || placement.RackID != 11 {
		t.Fatalf("placement not relocated: %+v err=%v", placement, err)
	}
	if result.Scenario.Maintenance == nil || result.Scenario.ScenarioStatus != constants.ScenarioDraft {
		t.Fatalf("expected readable maintenance draft, got %+v", result.Scenario)
	}
	if len(result.Scenario.Maintenance.Before) != 3 || len(result.Scenario.Maintenance.After) != 3 {
		t.Fatalf("expected frozen before/after, got %+v", result.Scenario.Maintenance)
	}
	var events int64
	db.Model(&audit.Event{}).Where("action = ?", "rack.maintenance.start").Count(&events)
	if events != 1 {
		t.Fatalf("expected one start audit event, got %d", events)
	}
}

func TestStartMaintenanceRejectionLeavesLayoutUntouched(t *testing.T) {
	service, db := rackMaintenanceFixture(t)
	_, err := service.StartMaintenance(context.Background(), 21, dto.StartMaintenanceRequest{Reason: "rail"}, rackTestActor())
	if err == nil {
		t.Fatal("expected capacity rejection")
	}
	appErr, ok := err.(*web.AppError)
	if !ok || appErr.Code != "MAINTENANCE_CAPACITY_INSUFFICIENT" {
		t.Fatalf("expected MAINTENANCE_CAPACITY_INSUFFICIENT, got %T %v", err, err)
	}
	var rack model.Rack
	if err := db.First(&rack, 21).Error; err != nil || rack.RackStatus != constants.RackReserved {
		t.Fatalf("rack status changed on rejection: %+v err=%v", rack, err)
	}
	var placement model.RackPlacement
	if err := db.Where("load_id = ?", 3).First(&placement).Error; err != nil || placement.RackID != 21 {
		t.Fatalf("placement moved on rejection: %+v err=%v", placement, err)
	}
	var scenarios, events int64
	db.Model(&model.LayoutScenario{}).Count(&scenarios)
	db.Model(&audit.Event{}).Where("action = ?", "rack.maintenance.reject").Count(&events)
	if scenarios != 0 || events != 1 {
		t.Fatalf("expected zero scenarios and one reject audit, got scenarios=%d events=%d", scenarios, events)
	}
}

func TestMaintenanceNotAllowedTwice(t *testing.T) {
	service, _ := rackMaintenanceFixture(t)
	ctx := context.Background()
	if _, err := service.StartMaintenance(ctx, 10, dto.StartMaintenanceRequest{}, rackTestActor()); err != nil {
		t.Fatalf("first maintenance: %v", err)
	}
	_, err := service.StartMaintenance(ctx, 10, dto.StartMaintenanceRequest{}, rackTestActor())
	if err == nil {
		t.Fatal("second maintenance on same rack must fail")
	}
	if appErr, ok := err.(*web.AppError); !ok || appErr.Code != "RACK_NOT_MAINTAINABLE" {
		t.Fatalf("expected RACK_NOT_MAINTAINABLE, got %v", err)
	}
}

func TestConcurrentMaintenanceSucceedsOnce(t *testing.T) {
	service, db := rackMaintenanceFixture(t)
	extra := model.Rack{ID: 12, ZoneID: 1, RackCode: "A-03", RowIndex: 1, ColumnIndex: 3, PowerLimitKW: 30, AirflowLimitCFM: 7200, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1}
	if err := db.Create(&extra).Error; err != nil {
		t.Fatalf("seed extra rack: %v", err)
	}
	const attempts = 8
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.StartMaintenance(context.Background(), 10, dto.StartMaintenanceRequest{}, rackTestActor())
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if appErr, ok := err.(*web.AppError); ok && (appErr.Code == "RACK_NOT_MAINTAINABLE" || appErr.Code == "RACK_MAINTENANCE_CONFLICT") {
			continue
		}
		t.Fatalf("unexpected concurrent error: %v", err)
	}
	if successes != 1 {
		t.Fatalf("expected exactly one success, got %d", successes)
	}
}
