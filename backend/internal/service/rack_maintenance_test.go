package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/auth"
	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type maintenanceFixture struct {
	db        *gorm.DB
	svc       *MaintenanceService
	racks     []model.Rack
	loads     []model.EquipmentLoad
	baseline  model.LayoutScenario
	auditRepo *audit.Repository
}

func newMaintenanceFixture(t *testing.T, dsn string, racks []model.Rack, loads []model.EquipmentLoad, baselineAssignments []dto.RackAssignment) maintenanceFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&auth.User{}, &model.ThermalZone{}, &model.Rack{}, &model.EquipmentLoad{}, &model.LayoutScenario{}, &audit.Event{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := auth.SeedUsers(db); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	zones := []model.ThermalZone{
		{ID: 1, ZoneCode: "TZ-A", Name: "A", CoolingCapacityKW: 120, SupplyTempC: 18, MaxReturnTempC: 32, AdjacencyJSON: `{}`, ZoneStatus: "active"},
		{ID: 2, ZoneCode: "TZ-B", Name: "B", CoolingCapacityKW: 120, SupplyTempC: 18, MaxReturnTempC: 32, AdjacencyJSON: `{}`, ZoneStatus: "active"},
	}
	if err := db.Create(&zones).Error; err != nil {
		t.Fatalf("seed zones: %v", err)
	}
	if err := db.Create(&racks).Error; err != nil {
		t.Fatalf("seed racks: %v", err)
	}
	if err := db.Create(&loads).Error; err != nil {
		t.Fatalf("seed loads: %v", err)
	}
	assignmentsJSON, _ := json.Marshal(baselineAssignments)
	baseline := model.LayoutScenario{
		Name: "approved-baseline-" + t.Name(), ScenarioStatus: constants.ScenarioApproved,
		RackAssignmentsJSON: string(assignmentsJSON), InputSnapshotJSON: `{"load_ids":[],"algorithm_version":"thermal-v1"}`,
		ZoneResultsJSON: "[]", ConstraintViolationsJSON: "[]", TotalPowerKW: 8, Score: 80,
		AlgorithmVersion: planner.AlgorithmVersion, Version: 3, CreatedBy: 1, ApprovedBy: ptrUint(1),
	}
	if err := db.Create(&baseline).Error; err != nil {
		t.Fatalf("seed baseline: %v", err)
	}

	auditRepo := audit.NewRepository(db)
	rackRepo := repository.NewRackRepository(db, auditRepo)
	zoneRepo := repository.NewThermalZoneRepository(db, auditRepo)
	loadRepo := repository.NewEquipmentLoadRepository(db, auditRepo)
	scenarioRepo := repository.NewLayoutScenarioRepository(db, auditRepo)
	maintRepo := repository.NewMaintenanceRepository(db, auditRepo)
	svc := NewMaintenanceService(rackRepo, scenarioRepo, loadRepo, zoneRepo, maintRepo)
	return maintenanceFixture{db: db, svc: svc, racks: racks, loads: loads, baseline: baseline, auditRepo: auditRepo}
}

func ptrUint(value uint) *uint { return &value }

func testActor() audit.Entry {
	return audit.Entry{RequestID: "req-maintenance-test", ActorID: 1, ActorUsername: "planner"}
}

func appErrorCode(t *testing.T, err error) string {
	t.Helper()
	var appErr *web.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	t.Fatalf("expected AppError, got %v", err)
	return ""
}

func TestMaintenanceStartSuccessCommitsDraftAndAudit(t *testing.T) {
	racks := []model.Rack{
		{ID: 1, ZoneID: 1, RackCode: "A-01", RowIndex: 1, ColumnIndex: 1, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 2, ZoneID: 1, RackCode: "A-02", RowIndex: 1, ColumnIndex: 2, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
	}
	loads := []model.EquipmentLoad{{ID: 10, Name: "node-10", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"}}
	assignments := []dto.RackAssignment{{LoadID: 10, LoadName: "node-10", RackID: 1, RackCode: "A-01", ZoneID: 1, ZoneCode: "TZ-A", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8}}
	fixture := newMaintenanceFixture(t, "file:mt-success?mode=memory&cache=shared", racks, loads, assignments)

	result, err := fixture.svc.Start(context.Background(), 1, dto.StartRackMaintenanceRequest{Reason: "fan swap"}, testActor())
	if err != nil {
		t.Fatalf("start maintenance: %v", err)
	}
	if result.DraftScenarioID == 0 {
		t.Fatal("expected a frozen draft scenario id")
	}
	if len(result.Moves) != 1 || result.Moves[0].ToRackID != 2 {
		t.Fatalf("unexpected moves: %+v", result.Moves)
	}
	if len(result.After) != 1 || result.After[0].RackID != 2 {
		t.Fatalf("migration not frozen on destination rack: %+v", result.After)
	}

	var rack model.Rack
	if err := fixture.db.First(&rack, 1).Error; err != nil {
		t.Fatalf("reload rack: %v", err)
	}
	if rack.RackStatus != constants.RackMaintenance {
		t.Fatalf("rack status = %s, want maintenance", rack.RackStatus)
	}
	if rack.Version != 2 {
		t.Fatalf("rack version = %d, want 2", rack.Version)
	}

	// The migration draft must be readable back as a normal draft scenario.
	draft, err := fixture.svc.scenarios.Get(context.Background(), result.DraftScenarioID)
	if err != nil {
		t.Fatalf("read draft back: %v", err)
	}
	if draft.ScenarioStatus != constants.ScenarioDraft {
		t.Fatalf("draft status = %s", draft.ScenarioStatus)
	}
	decoded := dto.DecodeScenario(draft)
	if decoded.Maintenance == nil || decoded.Maintenance.RackCode != "A-01" || decoded.Maintenance.SourceScenarioID != fixture.baseline.ID {
		t.Fatalf("draft missing maintenance provenance: %+v", decoded.Maintenance)
	}
	if len(decoded.Assignments) != 1 || decoded.Assignments[0].RackCode != "A-02" {
		t.Fatalf("frozen draft assignments wrong: %+v", decoded.Assignments)
	}

	events, _, err := fixture.auditRepo.List(context.Background(), audit.Filter{Page: 1, Size: 50})
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	actions := map[string]bool{}
	for _, event := range events {
		actions[event.Action] = true
	}
	if !actions["rack.maintenance.start"] || !actions["layout_scenario.maintenance_draft"] {
		t.Fatalf("missing commit audit events: %+v", actions)
	}
}

func TestMaintenanceStartRejectionLeavesStateUntouched(t *testing.T) {
	racks := []model.Rack{
		{ID: 1, ZoneID: 1, RackCode: "A-01", RowIndex: 1, ColumnIndex: 1, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 2, ZoneID: 1, RackCode: "A-02", RowIndex: 1, ColumnIndex: 2, PowerLimitKW: 10, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 3, ZoneID: 2, RackCode: "B-01", RowIndex: 2, ColumnIndex: 1, PowerLimitKW: 200, AirflowLimitCFM: 99999, RackUnits: 60, RackStatus: constants.RackAvailable, Version: 1},
	}
	loads := []model.EquipmentLoad{{ID: 11, Name: "node-11", PowerKW: 15, HeatKW: 14, AirflowCFM: 2000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"}}
	assignments := []dto.RackAssignment{{LoadID: 11, LoadName: "node-11", RackID: 1, RackCode: "A-01", ZoneID: 1, ZoneCode: "TZ-A", PowerKW: 15, HeatKW: 14, AirflowCFM: 2000, RackUnits: 8}}
	fixture := newMaintenanceFixture(t, "file:mt-reject?mode=memory&cache=shared", racks, loads, assignments)

	var scenariosBefore int64
	fixture.db.Model(&model.LayoutScenario{}).Count(&scenariosBefore)

	_, err := fixture.svc.Start(context.Background(), 1, dto.StartRackMaintenanceRequest{}, testActor())
	if code := appErrorCode(t, err); code != "MAINTENANCE_MIGRATION_REJECTED" {
		t.Fatalf("error code = %s, want MAINTENANCE_MIGRATION_REJECTED", code)
	}
	var appErr *web.AppError
	errors.As(err, &appErr)
	details, ok := appErr.Details.(maintenanceRejectionDetails)
	if !ok {
		t.Fatalf("rejection details missing or wrong type: %T", appErr.Details)
	}
	if len(details.Failures) == 0 || details.Failures[0].Code != "RACK_POWER_LIMIT" {
		t.Fatalf("expected RACK_POWER_LIMIT evidence, got %+v", details.Failures)
	}

	var rack model.Rack
	if err := fixture.db.First(&rack, 1).Error; err != nil {
		t.Fatalf("reload rack: %v", err)
	}
	if rack.RackStatus != constants.RackAvailable {
		t.Fatalf("rack status changed after rejection: %s", rack.RackStatus)
	}
	if rack.Version != 1 {
		t.Fatalf("rack version changed after rejection: %d", rack.Version)
	}
	var scenariosAfter int64
	fixture.db.Model(&model.LayoutScenario{}).Count(&scenariosAfter)
	if scenariosAfter != scenariosBefore {
		t.Fatalf("scenario count changed on rejection: before=%d after=%d", scenariosBefore, scenariosAfter)
	}
	events, _, _ := fixture.auditRepo.List(context.Background(), audit.Filter{Page: 1, Size: 50})
	found := false
	for _, event := range events {
		if event.Action == "rack.maintenance.reject" {
			found = true
		}
	}
	if !found {
		t.Fatal("rejection must still be audited")
	}
}

func TestMaintenanceStartTwiceOnlyOneSucceeds(t *testing.T) {
	racks := []model.Rack{
		{ID: 1, ZoneID: 1, RackCode: "A-01", RowIndex: 1, ColumnIndex: 1, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 2, ZoneID: 1, RackCode: "A-02", RowIndex: 1, ColumnIndex: 2, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
	}
	loads := []model.EquipmentLoad{{ID: 12, Name: "node-12", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"}}
	assignments := []dto.RackAssignment{{LoadID: 12, LoadName: "node-12", RackID: 1, RackCode: "A-01", ZoneID: 1, ZoneCode: "TZ-A", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8}}
	fixture := newMaintenanceFixture(t, "file:mt-twice?mode=memory&cache=shared", racks, loads, assignments)

	if _, err := fixture.svc.Start(context.Background(), 1, dto.StartRackMaintenanceRequest{}, testActor()); err != nil {
		t.Fatalf("first start: %v", err)
	}
	// A concurrent claimant (or a retry) must lose: the conditional status
	// update matches zero rows once the first commit landed.
	_, err := fixture.svc.Start(context.Background(), 1, dto.StartRackMaintenanceRequest{}, testActor())
	if code := appErrorCode(t, err); code != "RACK_STATUS_NOT_MAINTAINABLE" {
		t.Fatalf("second start code = %s, want RACK_STATUS_NOT_MAINTAINABLE", code)
	}

	var draftCount int64
	fixture.db.Model(&model.LayoutScenario{}).Where("scenario_status = ?", constants.ScenarioDraft).Count(&draftCount)
	if draftCount != 1 {
		t.Fatalf("expected exactly one migration draft, got %d", draftCount)
	}
}

func TestMaintenanceConcurrentStartsExactlyOneSucceeds(t *testing.T) {
	racks := []model.Rack{
		{ID: 1, ZoneID: 1, RackCode: "A-01", RowIndex: 1, ColumnIndex: 1, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 2, ZoneID: 1, RackCode: "A-02", RowIndex: 1, ColumnIndex: 2, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 3, ZoneID: 1, RackCode: "A-03", RowIndex: 1, ColumnIndex: 3, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
	}
	loads := []model.EquipmentLoad{{ID: 20, Name: "node-20", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"}}
	assignments := []dto.RackAssignment{{LoadID: 20, LoadName: "node-20", RackID: 1, RackCode: "A-01", ZoneID: 1, ZoneCode: "TZ-A", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8}}
	// WAL-backed file database so real connections contend on the rack claim
	// inside concurrent transactions (shared-cache in-memory SQLite deadlocks).
	fixture := newMaintenanceFixture(t, "file:"+filepath.Join(t.TempDir(), "mt-concurrent.db")+"?_journal_mode=WAL&_busy_timeout=10000", racks, loads, assignments)
	sqlDB, _ := fixture.db.DB()
	sqlDB.SetMaxOpenConns(8)

	const workers = 8
	type outcome struct {
		ok   bool
		code string
	}
	outcomes := make(chan outcome, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, err := fixture.svc.Start(context.Background(), 1, dto.StartRackMaintenanceRequest{}, testActor())
			if err == nil {
				outcomes <- outcome{ok: true}
				return
			}
			var appErr *web.AppError
			if errors.As(err, &appErr) {
				outcomes <- outcome{ok: false, code: appErr.Code}
				return
			}
			outcomes <- outcome{ok: false, code: "NON_APP_ERROR:" + err.Error()}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	successes, codes := 0, map[string]int{}
	for result := range outcomes {
		if result.ok {
			successes++
		} else {
			codes[result.code]++
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one concurrent start must succeed, got %d; failure codes=%v", successes, codes)
	}
	var rack model.Rack
	if err := fixture.db.First(&rack, 1).Error; err != nil {
		t.Fatalf("reload rack: %v", err)
	}
	if rack.Version != 2 || rack.RackStatus != constants.RackMaintenance {
		t.Fatalf("unexpected rack state after concurrent starts: status=%s version=%d", rack.RackStatus, rack.Version)
	}
	var draftCount int64
	fixture.db.Model(&model.LayoutScenario{}).Where("scenario_status = ?", constants.ScenarioDraft).Count(&draftCount)
	if draftCount != 1 {
		t.Fatalf("expected one frozen draft, got %d", draftCount)
	}
}

func TestMaintenanceStartRequiresApprovedBaseline(t *testing.T) {
	racks := []model.Rack{
		{ID: 1, ZoneID: 1, RackCode: "A-01", RowIndex: 1, ColumnIndex: 1, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
		{ID: 2, ZoneID: 1, RackCode: "A-02", RowIndex: 1, ColumnIndex: 2, PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable, Version: 1},
	}
	loads := []model.EquipmentLoad{{ID: 13, Name: "node-13", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8, RedundancyGroup: "G", LoadStatus: "ready"}}
	assignments := []dto.RackAssignment{{LoadID: 13, LoadName: "node-13", RackID: 1, RackCode: "A-01", ZoneID: 1, ZoneCode: "TZ-A", PowerKW: 8, HeatKW: 7.5, AirflowCFM: 2000, RackUnits: 8}}
	fixture := newMaintenanceFixture(t, "file:mt-baseline?mode=memory&cache=shared", racks, loads, assignments)
	if err := fixture.db.Model(&model.LayoutScenario{}).Where("id = ?", fixture.baseline.ID).Update("scenario_status", constants.ScenarioArchived).Error; err != nil {
		t.Fatalf("archive baseline: %v", err)
	}
	_, err := fixture.svc.Start(context.Background(), 1, dto.StartRackMaintenanceRequest{}, testActor())
	if code := appErrorCode(t, err); code != "MAINTENANCE_BASELINE_MISSING" {
		t.Fatalf("code = %s, want MAINTENANCE_BASELINE_MISSING", code)
	}
}
