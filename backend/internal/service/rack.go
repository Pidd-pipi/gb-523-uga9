package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"datacenter-thermal-capacity-planner/backend/internal/audit"
	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
	"datacenter-thermal-capacity-planner/backend/internal/planner"
	"datacenter-thermal-capacity-planner/backend/internal/repository"
	"datacenter-thermal-capacity-planner/backend/internal/web"
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
	usage, err := s.racks.PlacementUtilization(ctx)
	if err != nil {
		return nil, 0, err
	}
	responses := make([]dto.RackResponse, 0, len(racks))
	for _, rack := range racks {
		responses = append(responses, decodeRack(rack, usage[rack.ID]))
	}
	return responses, total, nil
}

func (s *RackService) Get(ctx context.Context, id uint) (dto.RackResponse, error) {
	rack, err := s.racks.Get(ctx, id)
	if err != nil {
		return dto.RackResponse{}, err
	}
	usage, err := s.racks.PlacementUtilization(ctx)
	if err != nil {
		return dto.RackResponse{}, err
	}
	return decodeRack(rack, usage[id]), nil
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

func decodeRack(rack model.Rack, usage dto.RackUtilization) dto.RackResponse {
	if rack.PowerLimitKW > 0 {
		usage.PowerPercent = usage.PowerKW / rack.PowerLimitKW * 100
	}
	if rack.AirflowLimitCFM > 0 {
		usage.AirflowPercent = usage.AirflowCFM / rack.AirflowLimitCFM * 100
	}
	if rack.RackUnits > 0 {
		usage.UnitsPercent = float64(usage.RackUnits) / float64(rack.RackUnits) * 100
	}
	return dto.RackResponse{
		ID: rack.ID, ZoneID: rack.ZoneID, ZoneCode: rack.ThermalZone.ZoneCode, RackCode: rack.RackCode,
		RowIndex: rack.RowIndex, ColumnIndex: rack.ColumnIndex, PowerLimitKW: rack.PowerLimitKW,
		AirflowLimitCFM: rack.AirflowLimitCFM, RackUnits: rack.RackUnits, RackStatus: rack.RackStatus,
		Version: rack.Version, Utilization: usage,
	}
}

// ListPlacements returns the current operational layout for the planner page.
func (s *RackService) ListPlacements(ctx context.Context) ([]dto.PlacementResponse, error) {
	placements, racks, loads, err := s.racks.ListPlacements(ctx)
	if err != nil {
		return nil, err
	}
	rackByID := make(map[uint]model.Rack, len(racks))
	loadByID := make(map[uint]model.EquipmentLoad, len(loads))
	for i := range racks {
		rackByID[racks[i].ID] = racks[i]
	}
	for i := range loads {
		loadByID[loads[i].ID] = loads[i]
	}
	responses := make([]dto.PlacementResponse, 0, len(placements))
	for _, placement := range placements {
		rack, load := rackByID[placement.RackID], loadByID[placement.LoadID]
		responses = append(responses, dto.PlacementResponse{
			ID: placement.ID, RackID: rack.ID, RackCode: rack.RackCode, ZoneID: rack.ZoneID,
			ZoneCode: rack.ThermalZone.ZoneCode, LoadID: load.ID, LoadName: load.Name,
			PowerKW: load.PowerKW, HeatKW: load.HeatKW, AirflowCFM: load.AirflowCFM,
			RackUnits: load.RackUnits, Source: placement.Source})
	}
	return responses, nil
}

// maintenanceLayout is the resolved live layout used to freeze a maintenance snapshot.
type maintenanceLayout struct {
	source      model.Rack
	racks       map[uint]model.Rack
	loads       map[uint]model.EquipmentLoad
	hosted      []planner.HostedEquipment
	sourceCount int
}

// StartMaintenance validates the rack, computes the migration and commits it atomically.
// Capacity failure returns a 422 carrying structured per-load evidence; rack status and
// placements remain unchanged.
func (s *RackService) StartMaintenance(ctx context.Context, rackID uint, req dto.StartMaintenanceRequest, actor audit.Entry) (dto.RackMaintenanceResponse, error) {
	source, err := s.racks.Get(ctx, rackID)
	if err != nil {
		return dto.RackMaintenanceResponse{}, err
	}
	if source.RackStatus != constants.RackAvailable && source.RackStatus != constants.RackReserved {
		return dto.RackMaintenanceResponse{}, web.Unprocessable("RACK_NOT_MAINTAINABLE",
			fmt.Sprintf("planner can only start maintenance on available or reserved racks; rack %s is %s", source.RackCode, source.RackStatus), nil)
	}
	if req.Version != 0 && source.Version != req.Version {
		return dto.RackMaintenanceResponse{}, web.Conflict("RACK_VERSION_CONFLICT", "rack was changed by another user before maintenance", nil)
	}

	plan, layout, err := s.computeMaintenancePlan(ctx, rackID)
	if err != nil {
		return dto.RackMaintenanceResponse{}, err
	}
	if !plan.Feasible {
		s.recordMaintenanceRejection(ctx, source, plan, req.Reason, actor)
		return dto.RackMaintenanceResponse{}, web.UnprocessableDetails("MAINTENANCE_CAPACITY_INSUFFICIENT",
			"maintenance rejected: hosted loads cannot all fit in same-zone racks with sufficient power, airflow and U-units",
			maintenanceFailureDetail(source, plan))
	}

	snapshot := buildMaintenanceSnapshot(layout, plan, strings.TrimSpace(req.Reason))
	scenario, err := newMaintenanceScenario(source, snapshot, actor.ActorID)
	if err != nil {
		return dto.RackMaintenanceResponse{}, err
	}

	entry := actor
	entry.Action = "rack.maintenance.start"
	entry.EntityType = "rack"
	entry.BeforeSummary = fmt.Sprintf("rack=%s status=%s hosted_loads=%d version=%d", source.RackCode, source.RackStatus, layout.sourceCount, source.Version)
	entry.AfterSummary = fmt.Sprintf("rack=%s status=%s moved=%d scenario=%s", source.RackCode, constants.RackMaintenance, len(plan.Moves), scenario.Name)

	committed, execErr := s.racks.ExecuteMaintenance(ctx, repository.MaintenanceInput{
		SourceRack: source, Scenario: scenario, ExpectedVersion: req.Version,
		CheckVersion: req.Version != 0, Audit: entry,
	})
	if execErr != nil {
		var infeasible *repository.MaintenanceInfeasible
		if errors.As(execErr, &infeasible) {
			s.recordMaintenanceRejection(ctx, source, infeasible.Plan, req.Reason, actor)
			return dto.RackMaintenanceResponse{}, web.UnprocessableDetails("MAINTENANCE_CAPACITY_INSUFFICIENT",
				"maintenance rejected after concurrent layout change: capacity no longer sufficient",
				maintenanceFailureDetail(source, infeasible.Plan))
		}
		return dto.RackMaintenanceResponse{}, execErr
	}

	moves := maintenanceMoveViews(committed.Moves)
	return dto.RackMaintenanceResponse{
		RackID: source.ID, RackCode: source.RackCode, ZoneID: source.ZoneID, ZoneCode: source.ThermalZone.ZoneCode,
		RackStatus: string(constants.RackMaintenance), RackVersion: source.Version + 1, MovedLoads: len(committed.Moves),
		Moves: moves, Before: snapshot.Before, After: snapshot.After, Scenario: dto.DecodeScenario(*scenario),
	}, nil
}

func (s *RackService) computeMaintenancePlan(ctx context.Context, rackID uint) (planner.MaintenancePlan, maintenanceLayout, error) {
	zones, err := s.zones.All(ctx)
	if err != nil {
		return planner.MaintenancePlan{}, maintenanceLayout{}, err
	}
	placements, racks, loads, err := s.racks.ListPlacements(ctx)
	if err != nil {
		return planner.MaintenancePlan{}, maintenanceLayout{}, err
	}
	loadByID := make(map[uint]model.EquipmentLoad, len(loads))
	for _, load := range loads {
		loadByID[load.ID] = load
	}
	rackByID := make(map[uint]model.Rack, len(racks))
	hosted := make([]planner.HostedEquipment, 0, len(placements))
	sourceCount := 0
	for _, placement := range placements {
		if load, ok := loadByID[placement.LoadID]; ok {
			hosted = append(hosted, planner.HostedEquipment{RackID: placement.RackID, Load: load})
			if placement.RackID == rackID {
				sourceCount++
			}
		}
	}
	for _, rack := range racks {
		rackByID[rack.ID] = rack
	}
	plan := planner.PlanMaintenance(zones, racks, hosted, rackID)
	return plan, maintenanceLayout{source: rackByID[rackID], racks: rackByID, loads: loadByID, hosted: hosted, sourceCount: sourceCount}, nil
}

func (s *RackService) recordMaintenanceRejection(ctx context.Context, source model.Rack, plan planner.MaintenancePlan, reason string, actor audit.Entry) {
	entry := actor
	entry.Action = "rack.maintenance.reject"
	entry.EntityType = "rack"
	entry.EntityID = source.ID
	entry.BeforeSummary = fmt.Sprintf("rack=%s status=%s", source.RackCode, source.RackStatus)
	codes := map[string]bool{}
	for _, failure := range plan.LoadFailures {
		for _, rejection := range failure.CandidateRejections {
			for _, shortfall := range rejection.Shortfalls {
				codes[shortfall.Code] = true
			}
		}
	}
	evidence := make([]string, 0, len(codes))
	for code := range codes {
		evidence = append(evidence, code)
	}
	sort.Strings(evidence)
	entry.AfterSummary = fmt.Sprintf("rejected failures=%d evidence=%v reason=%s", len(plan.LoadFailures), evidence, strings.TrimSpace(reason))
	_ = s.racks.RecordMaintenanceRejection(ctx, entry)
}

// maintenanceFailureDetail is the structured 422 payload reusing planner evidence types.
func maintenanceFailureDetail(source model.Rack, plan planner.MaintenancePlan) any {
	return struct {
		Code                string                           `json:"code"`
		MaintenanceRackID   uint                             `json:"maintenance_rack_id"`
		MaintenanceRackCode string                           `json:"maintenance_rack_code"`
		ZoneID              uint                             `json:"zone_id"`
		ZoneCode            string                           `json:"zone_code"`
		LoadFailures        []planner.MaintenanceLoadFailure `json:"load_failures"`
	}{
		Code: "MAINTENANCE_CAPACITY_INSUFFICIENT", MaintenanceRackID: source.ID,
		MaintenanceRackCode: source.RackCode, ZoneID: source.ZoneID, ZoneCode: source.ThermalZone.ZoneCode,
		LoadFailures: plan.LoadFailures,
	}
}

func newMaintenanceScenario(source model.Rack, snapshot dto.MaintenanceSnapshot, actorID uint) (*model.LayoutScenario, error) {
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, web.Internal(fmt.Errorf("encode maintenance snapshot: %w", err))
	}
	assignments := make([]map[string]any, 0, len(snapshot.After))
	for _, placement := range snapshot.After {
		assignments = append(assignments, map[string]any{
			"load_id": placement.LoadID, "load_name": placement.LoadName, "rack_id": placement.RackID,
			"rack_code": placement.RackCode, "zone_id": placement.ZoneID, "zone_code": placement.ZoneCode,
			"power_kw": placement.PowerKW, "airflow_cfm": placement.AirflowCFM, "rack_units": placement.RackUnits,
			"placement_score": 0.0, "explanation": []string{"relocated by rack maintenance migration"},
		})
	}
	assignmentsJSON, err := json.Marshal(assignments)
	if err != nil {
		return nil, web.Internal(fmt.Errorf("encode maintenance assignments: %w", err))
	}
	return &model.LayoutScenario{
		Name: fmt.Sprintf("MAINT-%s-%d", source.RackCode, source.ID), ScenarioStatus: constants.ScenarioDraft,
		RackAssignmentsJSON: string(assignmentsJSON), InputSnapshotJSON: string(snapshotJSON),
		ZoneResultsJSON: "[]", ConstraintViolationsJSON: "[]",
		AlgorithmVersion: planner.MaintenanceAlgorithmVersion, Version: 1, CreatedBy: actorID,
	}, nil
}

func buildMaintenanceSnapshot(layout maintenanceLayout, plan planner.MaintenancePlan, reason string) dto.MaintenanceSnapshot {
	before := make([]dto.RackPlacementView, 0, len(layout.hosted))
	after := []dto.RackPlacementView{}
	for _, hosted := range layout.hosted {
		rack := layout.racks[hosted.RackID]
		before = append(before, toPlacementView(rack, hosted.Load))
		if rack.ID == layout.source.ID {
			continue
		}
		after = append(after, toPlacementView(rack, hosted.Load))
	}
	for _, move := range plan.Moves {
		after = append(after, toPlacementView(layout.racks[move.ToRackID], layout.loads[move.LoadID]))
	}
	sort.Slice(before, func(i, j int) bool { return placementViewLess(before[i], before[j]) })
	sort.Slice(after, func(i, j int) bool { return placementViewLess(after[i], after[j]) })
	return dto.MaintenanceSnapshot{
		Kind: "rack_maintenance", AlgorithmVersion: planner.MaintenanceAlgorithmVersion,
		MaintenanceRackID: layout.source.ID, MaintenanceRackCode: layout.source.RackCode,
		ZoneID: layout.source.ZoneID, ZoneCode: layout.source.ThermalZone.ZoneCode,
		Reason: reason, Before: before, After: after, Moves: maintenanceMoveViews(plan.Moves),
	}
}

func maintenanceMoveViews(moves []planner.MaintenanceMove) []dto.MaintenanceMove {
	out := make([]dto.MaintenanceMove, 0, len(moves))
	for _, move := range moves {
		out = append(out, dto.MaintenanceMove{
			LoadID: move.LoadID, LoadName: move.LoadName, FromRackID: move.FromRackID, FromRackCode: move.FromRackCode,
			ToRackID: move.ToRackID, ToRackCode: move.ToRackCode, ZoneID: move.ZoneID, ZoneCode: move.ZoneCode,
			PowerKW: move.PowerKW, HeatKW: move.HeatKW, AirflowCFM: move.AirflowCFM, RackUnits: move.RackUnits})
	}
	return out
}

func toPlacementView(rack model.Rack, load model.EquipmentLoad) dto.RackPlacementView {
	return dto.RackPlacementView{LoadID: load.ID, LoadName: load.Name, RackID: rack.ID, RackCode: rack.RackCode,
		ZoneID: rack.ZoneID, ZoneCode: rack.ThermalZone.ZoneCode, PowerKW: load.PowerKW, AirflowCFM: load.AirflowCFM, RackUnits: load.RackUnits}
}

func placementViewLess(a, b dto.RackPlacementView) bool {
	return a.RackID < b.RackID || a.RackID == b.RackID && a.LoadID < b.LoadID
}
