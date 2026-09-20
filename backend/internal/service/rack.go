package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/web"
	"gorm.io/gorm"
)

type RackService struct {
	racks *repository.RackRepository
	zones *repository.ThermalZoneRepository
}

func NewRackService(racks *repository.RackRepository, zones *repository.ThermalZoneRepository) *RackService {
	return &RackService{racks: racks, zones: zones}
}

func (s *RackService) List(ctx context.Context, search, status string, zoneID uint, page, size int) ([]dto.RackResponse, int64, error) {
	racks, total, err := s.racks.List(ctx, search, status, zoneID, page, size)
	if err != nil {
		return nil, 0, err
	}
	responses := make([]dto.RackResponse, 0, len(racks))
	for _, rack := range racks {
		responses = append(responses, decodeRack(rack))
	}
	return responses, total, nil
}

func (s *RackService) Get(ctx context.Context, id uint) (dto.RackResponse, error) {
	rack, err := s.racks.Get(ctx, id)
	if err != nil {
		return dto.RackResponse{}, err
	}
	return decodeRack(rack), nil
}

func (s *RackService) Create(ctx context.Context, req dto.CreateRackRequest, actor audit.Entry) (dto.RackResponse, error) {
	rack, err := dto.NewRack(req)
	if err != nil {
		return dto.RackResponse{}, web.Unprocessable("INVALID_RACK", err.Error(), err)
	}
	actor.Action = "rack.create"
	actor.EntityType = "rack"
	actor.AfterSummary = fmt.Sprintf("%s zone=%d position=%d,%d power=%.2f airflow=%.2f", rack.RackCode, rack.ZoneID, rack.RowIndex, rack.ColumnIndex, rack.PowerLimitKW, rack.AirflowLimitCFM)
	if err := s.racks.Create(ctx, &rack, actor); err != nil {
		return dto.RackResponse{}, err
	}
	return s.Get(ctx, rack.ID)
}

func (s *RackService) Update(ctx context.Context, id uint, req dto.UpdateRackRequest, actor audit.Entry) (dto.RackResponse, error) {
	if err := req.ValidateBusiness(); err != nil {
		return dto.RackResponse{}, web.Unprocessable("INVALID_RACK", err.Error(), err)
	}
	current, err := s.racks.Get(ctx, id)
	if err != nil {
		return dto.RackResponse{}, err
	}
	if _, err := s.zones.Get(ctx, req.ZoneID); err != nil {
		return dto.RackResponse{}, web.Unprocessable("ZONE_NOT_FOUND", "selected thermal zone does not exist", err)
	}
	updated := model.Rack{
		ID: id, ZoneID: req.ZoneID, RackCode: current.RackCode, RowIndex: req.RowIndex,
		ColumnIndex: req.ColumnIndex, PowerLimitKW: req.PowerLimitKW, AirflowLimitCFM: req.AirflowLimitCFM,
		RackUnits: req.RackUnits, RackStatus: req.RackStatus, Version: req.Version,
	}
	actor.Action = "rack.update"
	actor.EntityType = "rack"
	actor.BeforeSummary = fmt.Sprintf("zone=%d position=%d,%d version=%d", current.ZoneID, current.RowIndex, current.ColumnIndex, current.Version)
	actor.AfterSummary = fmt.Sprintf("zone=%d position=%d,%d version=%d", req.ZoneID, req.RowIndex, req.ColumnIndex, req.Version+1)
	if err := s.racks.Update(ctx, &updated, req.Version, actor); err != nil {
		return dto.RackResponse{}, err
	}
	return s.Get(ctx, id)
}

func decodeRack(rack model.Rack) dto.RackResponse {
	return dto.RackResponse{
		ID: rack.ID, ZoneID: rack.ZoneID, ZoneCode: rack.ThermalZone.ZoneCode, RackCode: rack.RackCode,
		RowIndex: rack.RowIndex, ColumnIndex: rack.ColumnIndex, PowerLimitKW: rack.PowerLimitKW,
		AirflowLimitCFM: rack.AirflowLimitCFM, RackUnits: rack.RackUnits, RackStatus: rack.RackStatus,
		Version: rack.Version, Utilization: dto.RackUtilization{},
	}
}

// MaintenanceService closes the rack maintenance migration loop: resolve the
// approved baseline, run the deterministic migration planner, then either
// reject with capacity evidence or commit rack status plus the frozen draft in
// one transaction.
type MaintenanceService struct {
	racks       *repository.RackRepository
	scenarios   *repository.LayoutScenarioRepository
	loads       *repository.EquipmentLoadRepository
	zones       *repository.ThermalZoneRepository
	maintenance *repository.MaintenanceRepository
}

func NewMaintenanceService(racks *repository.RackRepository, scenarios *repository.LayoutScenarioRepository, loads *repository.EquipmentLoadRepository, zones *repository.ThermalZoneRepository, maintenance *repository.MaintenanceRepository) *MaintenanceService {
	return &MaintenanceService{racks: racks, scenarios: scenarios, loads: loads, zones: zones, maintenance: maintenance}
}

type maintenanceRejectionDetails struct {
	RackID           uint                              `json:"rack_id"`
	RackCode         string                            `json:"rack_code"`
	ZoneID           uint                              `json:"zone_id"`
	ZoneCode         string                            `json:"zone_code"`
	SourceScenarioID uint                              `json:"source_scenario_id"`
	Failures         []dto.MaintenanceFailureResponse  `json:"failures"`
	RackViews        []dto.MaintenanceRackViewResponse `json:"rack_views"`
	Before           []dto.RackAssignment              `json:"before"`
}

func (s *MaintenanceService) Start(ctx context.Context, rackID uint, req dto.StartRackMaintenanceRequest, actor audit.Entry) (dto.MaintenanceMigrationResponse, error) {
	if err := req.ValidateBusiness(); err != nil {
		return dto.MaintenanceMigrationResponse{}, web.Unprocessable("INVALID_MAINTENANCE_REQUEST", err.Error(), err)
	}
	rack, err := s.racks.Get(ctx, rackID)
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	if !constants.RackCanReceiveLoad(rack.RackStatus) {
		return dto.MaintenanceMigrationResponse{}, web.Unprocessable("RACK_STATUS_NOT_MAINTAINABLE",
			fmt.Sprintf("rack %s is %s; maintenance can only start from available or reserved racks", rack.RackCode, rack.RackStatus), nil)
	}
	baseline, err := s.scenarios.LatestApproved(ctx, req.SourceScenarioID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if req.SourceScenarioID != nil {
				return dto.MaintenanceMigrationResponse{}, web.Unprocessable("MAINTENANCE_BASELINE_NOT_APPROVED", "the specified source scenario is not an approved layout baseline", nil)
			}
			return dto.MaintenanceMigrationResponse{}, web.Unprocessable("MAINTENANCE_BASELINE_MISSING", "no approved layout scenario exists; approve a scenario before starting rack maintenance", nil)
		}
		return dto.MaintenanceMigrationResponse{}, err
	}
	beforeAssignments := []dto.RackAssignment{}
	if err := json.Unmarshal([]byte(baseline.RackAssignmentsJSON), &beforeAssignments); err != nil {
		return dto.MaintenanceMigrationResponse{}, web.Unprocessable("INVALID_BASELINE_LAYOUT", "approved baseline assignments cannot be decoded", err)
	}
	rackLoads := []uint{}
	for _, assignment := range beforeAssignments {
		if assignment.RackID == rackID {
			rackLoads = append(rackLoads, assignment.LoadID)
		}
	}
	if len(rackLoads) == 0 {
		return dto.MaintenanceMigrationResponse{}, web.Unprocessable("RACK_HAS_NO_PLANNED_LOADS",
			fmt.Sprintf("rack %s carries no planned loads in the approved baseline; change its status from the rack page instead", rack.RackCode), nil)
	}
	allZones, err := s.zones.All(ctx)
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	allRacks, err := s.racks.All(ctx)
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	rackLoadModels, err := s.loads.FindByIDs(ctx, rackLoads)
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}

	plan := planner.PlanMaintenance(allZones, allRacks, rackLoadModels, beforeAssignments, rackID)
	zoneCode := rack.ThermalZone.ZoneCode
	if !plan.Feasible {
		details := maintenanceRejectionDetails{RackID: rack.ID, RackCode: rack.RackCode, ZoneID: rack.ZoneID, ZoneCode: zoneCode, SourceScenarioID: baseline.ID, Before: beforeAssignments, Failures: []dto.MaintenanceFailureResponse{}, RackViews: []dto.MaintenanceRackViewResponse{}}
		for _, failure := range plan.Failures {
			details.Failures = append(details.Failures, dto.MaintenanceFailureResponse{LoadID: failure.LoadID, LoadName: failure.LoadName, RackID: failure.RackID, RackCode: failure.RackCode, Code: failure.Code, Message: failure.Message, Actual: failure.Actual, Limit: failure.Limit})
		}
		for _, view := range plan.RackViews {
			details.RackViews = append(details.RackViews, dto.MaintenanceRackViewResponse(view))
		}
		_ = s.maintenance.RecordRejection(ctx, rack, audit.Entry{
			RequestID: actor.RequestID, ActorID: actor.ActorID, ActorUsername: actor.ActorUsername,
			Action: "rack.maintenance.reject", EntityType: "rack", EntityID: rack.ID,
			BeforeSummary: fmt.Sprintf("status=%s baseline=%s loads=%d", rack.RackStatus, baseline.Name, len(rackLoads)),
			AfterSummary:  fmt.Sprintf("rejected failures=%d reason=%s", len(details.Failures), strings.TrimSpace(req.Reason)),
		})
		return dto.MaintenanceMigrationResponse{}, web.UnprocessableWithDetails("MAINTENANCE_MIGRATION_REJECTED",
			"maintenance migration rejected: same-zone racks cannot satisfy power, airflow or rack-unit capacity", details, nil)
	}

	loadIDs := uniqueAssignmentLoadIDs(plan.Assignments)
	snapshotModels, err := s.loads.FindByIDs(ctx, loadIDs)
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	draftName := fmt.Sprintf("Maintenance %s %s", rack.RackCode, time.Now().UTC().Format("20060102T150405Z"))
	snapshot := dto.ScenarioSnapshot{LoadIDs: loadIDs, AlgorithmVersion: planner.AlgorithmVersion, Maintenance: &dto.MaintenanceSnapshotMeta{RackID: rack.ID, RackCode: rack.RackCode, SourceScenarioID: baseline.ID, SourceScenarioName: baseline.Name, EvacuatedLoadCount: len(plan.Moves)}}
	snapshotJSON, err := encodeMaintenanceSnapshot(snapshot, allZones, allRacks, snapshotModels)
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	marshal := func(value any, label string) string {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			err = web.Internal(fmt.Errorf("encode maintenance %s: %w", label, marshalErr))
			return ""
		}
		return string(raw)
	}
	assignmentsJSON := marshal(plan.Assignments, "assignments")
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	zoneResultsJSON := marshal(plan.ZoneResults, "zone results")
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	violationsJSON := marshal(plan.Violations, "violations")
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	draft, err := s.maintenance.ApplyMaintenance(ctx, rack, repository.MaintenanceDraftInput{
		Name: draftName, RackAssignmentsJSON: assignmentsJSON, InputSnapshotJSON: snapshotJSON,
		ZoneResultsJSON: zoneResultsJSON, ConstraintViolationsJSON: violationsJSON,
		TotalPowerKW: plan.TotalPowerKW, PeakTempC: plan.PeakTempC, Score: plan.Score, ActorID: actor.ActorID,
	}, audit.Entry{
		RequestID: actor.RequestID, ActorID: actor.ActorID, ActorUsername: actor.ActorUsername,
		Action: "rack.maintenance.start", EntityType: "rack", EntityID: rack.ID,
		BeforeSummary: fmt.Sprintf("status=%s version=%d loads=%d", rack.RackStatus, rack.Version, len(rackLoads)),
		AfterSummary:  fmt.Sprintf("status=%s evacuated=%d baseline=%s", constants.RackMaintenance, len(plan.Moves), baseline.Name),
	}, audit.Entry{
		RequestID: actor.RequestID, ActorID: actor.ActorID, ActorUsername: actor.ActorUsername,
		Action: "layout_scenario.maintenance_draft", EntityType: "layout_scenario",
		BeforeSummary: fmt.Sprintf("baseline=%s(%d)", baseline.Name, baseline.ID),
		AfterSummary:  fmt.Sprintf("draft=%s moves=%d score=%.2f", draftName, len(plan.Moves), plan.Score),
	})
	if err != nil {
		return dto.MaintenanceMigrationResponse{}, err
	}
	return buildMaintenanceResponse(rack, zoneCode, baseline.ID, draft, strings.TrimSpace(req.Reason), beforeAssignments, plan), nil
}

func buildMaintenanceResponse(rack model.Rack, zoneCode string, sourceID uint, draft model.LayoutScenario, reason string, before []dto.RackAssignment, plan planner.MaintenancePlan) dto.MaintenanceMigrationResponse {
	response := dto.MaintenanceMigrationResponse{
		RackID: rack.ID, RackCode: rack.RackCode, ZoneID: rack.ZoneID, ZoneCode: zoneCode,
		SourceScenarioID: sourceID, DraftScenarioID: draft.ID, DraftName: draft.Name, Reason: reason,
		Moves: []dto.MaintenanceMoveResponse{}, RackViews: []dto.MaintenanceRackViewResponse{}, Before: before,
		After: plan.Assignments, ZoneResults: plan.ZoneResults, Violations: plan.Violations,
		Failures: []dto.MaintenanceFailureResponse{}, TotalPowerKW: plan.TotalPowerKW, PeakTempC: plan.PeakTempC, Score: plan.Score,
	}
	for _, move := range plan.Moves {
		response.Moves = append(response.Moves, dto.MaintenanceMoveResponse{
			LoadID: move.LoadID, LoadName: move.LoadName, FromRackID: move.FromRackID, FromRackCode: move.FromRackCode,
			ToRackID: move.ToRackID, ToRackCode: move.ToRackCode, ZoneID: move.ZoneID, ZoneCode: move.ZoneCode,
			PowerKW: move.PowerKW, HeatKW: move.HeatKW, AirflowCFM: move.AirflowCFM, RackUnits: move.RackUnits, PlacementScore: move.PlacementScore,
		})
	}
	for _, view := range plan.RackViews {
		response.RackViews = append(response.RackViews, dto.MaintenanceRackViewResponse(view))
	}
	for _, failure := range plan.Failures {
		response.Failures = append(response.Failures, dto.MaintenanceFailureResponse{LoadID: failure.LoadID, LoadName: failure.LoadName, RackID: failure.RackID, RackCode: failure.RackCode, Code: failure.Code, Message: failure.Message, Actual: failure.Actual, Limit: failure.Limit})
	}
	return response
}

func uniqueAssignmentLoadIDs(assignments []dto.RackAssignment) []uint {
	seen, ids := map[uint]bool{}, make([]uint, 0, len(assignments))
	for _, assignment := range assignments {
		if !seen[assignment.LoadID] {
			seen[assignment.LoadID] = true
			ids = append(ids, assignment.LoadID)
		}
	}
	return ids
}

func encodeMaintenanceSnapshot(snapshot dto.ScenarioSnapshot, zones []model.ThermalZone, racks []model.Rack, loads []model.EquipmentLoad) (string, error) {
	raw, err := json.Marshal(struct {
		dto.ScenarioSnapshot
		Zones []model.ThermalZone   `json:"zones"`
		Racks []model.Rack          `json:"racks"`
		Loads []model.EquipmentLoad `json:"loads"`
	}{ScenarioSnapshot: snapshot, Zones: zones, Racks: racks, Loads: loads})
	if err != nil {
		return "", web.Internal(fmt.Errorf("encode maintenance snapshot: %w", err))
	}
	return string(raw), nil
}
