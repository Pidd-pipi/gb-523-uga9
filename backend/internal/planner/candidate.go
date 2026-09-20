package planner

import (
	"sort"

	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
)

const AlgorithmVersion = "thermal-v1"

type Engine struct {
	maxIterations int
}

type Result struct {
	Assignments []dto.RackAssignment
	ZoneResults []dto.ZoneThermalResult
	Violations  []dto.ConstraintViolation
	TotalPower  float64
	PeakTemp    float64
	Score       float64
}

type rackUsage struct {
	powerKW    float64
	heatKW     float64
	airflowCFM float64
	rackUnits  int
	groups     map[string]bool
}

type candidate struct {
	rack        model.Rack
	zone        model.ThermalZone
	score       float64
	explanation []string
}

func NewEngine(maxIterations int) *Engine {
	if maxIterations < 1 {
		maxIterations = 1
	}
	return &Engine{maxIterations: maxIterations}
}

func (e *Engine) Evaluate(zones []model.ThermalZone, racks []model.Rack, loads []model.EquipmentLoad) Result {
	zoneByID := make(map[uint]model.ThermalZone, len(zones))
	for _, zone := range zones {
		zoneByID[zone.ID] = zone
	}

	orderedRacks := append([]model.Rack(nil), racks...)
	sort.SliceStable(orderedRacks, func(i, j int) bool {
		if orderedRacks[i].RackCode == orderedRacks[j].RackCode {
			return orderedRacks[i].ID < orderedRacks[j].ID
		}
		return orderedRacks[i].RackCode < orderedRacks[j].RackCode
	})
	orderedLoads := append([]model.EquipmentLoad(nil), loads...)
	sort.SliceStable(orderedLoads, func(i, j int) bool {
		left := tightness(orderedLoads[i], orderedRacks)
		right := tightness(orderedLoads[j], orderedRacks)
		if left == right {
			if orderedLoads[i].PowerKW == orderedLoads[j].PowerKW {
				return orderedLoads[i].ID < orderedLoads[j].ID
			}
			return orderedLoads[i].PowerKW > orderedLoads[j].PowerKW
		}
		return left > right
	})

	usage := make(map[uint]*rackUsage, len(orderedRacks))
	zoneHeat := make(map[uint]float64, len(zones))
	zonePower := make(map[uint]float64, len(zones))
	zoneGroups := make(map[uint]map[string]bool, len(zones))
	for _, zone := range zones {
		zoneGroups[zone.ID] = map[string]bool{}
	}
	for _, rack := range orderedRacks {
		usage[rack.ID] = &rackUsage{groups: map[string]bool{}}
	}

	result := Result{Assignments: []dto.RackAssignment{}, ZoneResults: []dto.ZoneThermalResult{}, Violations: []dto.ConstraintViolation{}}
	iterations := 0
	for _, load := range orderedLoads {
		if !load.IsPlannable() {
			result.Violations = append(result.Violations, dto.ConstraintViolation{
				Code: "LOAD_NOT_READY", Severity: "critical", EntityType: "equipment_load", EntityID: load.ID,
				Message: "load is not in ready state and cannot be placed",
			})
			continue
		}
		candidates := make([]candidate, 0, len(orderedRacks))
		var evidence []dto.ConstraintViolation
		for _, rack := range orderedRacks {
			iterations++
			if iterations > e.maxIterations {
				evidence = append(evidence, dto.ConstraintViolation{
					Code: "ITERATION_LIMIT", Severity: "critical", EntityType: "equipment_load", EntityID: load.ID,
					Message: "candidate search reached the configured iteration limit",
				})
				break
			}
			zone, exists := zoneByID[rack.ZoneID]
			if !exists {
				continue
			}
			violations := checkCandidate(load, rack, zone, usage[rack.ID], zoneHeat[rack.ZoneID], zoneGroups[rack.ZoneID])
			if len(violations) > 0 {
				evidence = append(evidence, violations...)
				continue
			}
			score, explanation := placementScore(load, rack, zone, usage[rack.ID], zoneHeat[rack.ZoneID], zones, zoneHeat)
			candidates = append(candidates, candidate{rack: rack, zone: zone, score: score, explanation: explanation})
		}
		if len(candidates) == 0 {
			result.Violations = append(result.Violations, summarizeUnplaced(load, evidence)...)
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].score == candidates[j].score {
				return candidates[i].rack.RackCode < candidates[j].rack.RackCode
			}
			return candidates[i].score > candidates[j].score
		})
		selected := candidates[0]
		u := usage[selected.rack.ID]
		u.powerKW += load.PowerKW
		u.heatKW += load.HeatKW
		u.airflowCFM += load.AirflowCFM
		u.rackUnits += load.RackUnits
		u.groups[load.RedundancyGroup] = true
		zoneGroups[selected.zone.ID][load.RedundancyGroup] = true
		zoneHeat[selected.zone.ID] += load.HeatKW
		zonePower[selected.zone.ID] += load.PowerKW
		result.TotalPower += load.PowerKW
		result.Assignments = append(result.Assignments, dto.RackAssignment{
			LoadID: load.ID, LoadName: load.Name, RackID: selected.rack.ID, RackCode: selected.rack.RackCode,
			ZoneID: selected.zone.ID, ZoneCode: selected.zone.ZoneCode, PowerKW: load.PowerKW,
			HeatKW: load.HeatKW, AirflowCFM: load.AirflowCFM, RackUnits: load.RackUnits,
			PlacementScore: selected.score, Explanation: selected.explanation,
		})
	}

	thermalResults, thermalViolations, peak := propagateThermal(zones, zoneHeat)
	result.ZoneResults = thermalResults
	result.Violations = append(result.Violations, thermalViolations...)
	result.PeakTemp = peak
	result.Violations = append(result.Violations, validateFinalAssignments(orderedRacks, usage, zones, zonePower, result.Assignments)...)
	result.Score = scenarioScore(result.Assignments, result.ZoneResults, result.Violations)
	return result
}

func tightness(load model.EquipmentLoad, racks []model.Rack) float64 {
	best := 0.0
	for _, rack := range racks {
		if !rack.IsUsable() {
			continue
		}
		value := maxFloat(load.PowerKW/rack.PowerLimitKW, load.AirflowCFM/rack.AirflowLimitCFM, float64(load.RackUnits)/float64(rack.RackUnits))
		if value > best {
			best = value
		}
	}
	return best
}

// MaintenanceFailure explains why one load could not be placed on a candidate
// rack during a maintenance migration.
type MaintenanceFailure struct {
	LoadID   uint    `json:"load_id"`
	LoadName string  `json:"load_name"`
	RackID   uint    `json:"rack_id"`
	RackCode string  `json:"rack_code"`
	Code     string  `json:"code"`
	Message  string  `json:"message"`
	Actual   float64 `json:"actual"`
	Limit    float64 `json:"limit"`
}

type MaintenanceMove struct {
	LoadID         uint    `json:"load_id"`
	LoadName       string  `json:"load_name"`
	FromRackID     uint    `json:"from_rack_id"`
	FromRackCode   string  `json:"from_rack_code"`
	ToRackID       uint    `json:"to_rack_id"`
	ToRackCode     string  `json:"to_rack_code"`
	ZoneID         uint    `json:"zone_id"`
	ZoneCode       string  `json:"zone_code"`
	PowerKW        float64 `json:"power_kw"`
	HeatKW         float64 `json:"heat_kw"`
	AirflowCFM     float64 `json:"airflow_cfm"`
	RackUnits      int     `json:"rack_units"`
	PlacementScore float64 `json:"placement_score"`
}

// MaintenanceRackView carries before/after occupancy of touched racks.
type MaintenanceRackView struct {
	RackID           uint    `json:"rack_id"`
	RackCode         string  `json:"rack_code"`
	ZoneID           uint    `json:"zone_id"`
	ZoneCode         string  `json:"zone_code"`
	RackStatus       string  `json:"rack_status"`
	BeforePowerKW    float64 `json:"before_power_kw"`
	AfterPowerKW     float64 `json:"after_power_kw"`
	BeforeAirflowCFM float64 `json:"before_airflow_cfm"`
	AfterAirflowCFM  float64 `json:"after_airflow_cfm"`
	BeforeRackUnits  int     `json:"before_rack_units"`
	AfterRackUnits   int     `json:"after_rack_units"`
	PowerLimitKW     float64 `json:"power_limit_kw"`
	AirflowLimitCFM  float64 `json:"airflow_limit_cfm"`
	RackUnitLimit    int     `json:"rack_unit_limit"`
}

type MaintenancePlan struct {
	Moves        []MaintenanceMove
	Failures     []MaintenanceFailure
	Assignments  []dto.RackAssignment
	ZoneResults  []dto.ZoneThermalResult
	Violations   []dto.ConstraintViolation
	RackViews    []MaintenanceRackView
	TotalPowerKW float64
	PeakTempC    float64
	Score        float64
	Feasible     bool
}

type maintenanceState struct {
	powerKW, heatKW, airflowCFM float64
	rackUnits                   int
}

type maintenanceCandidate struct {
	rack  model.Rack
	score float64
}

// PlanMaintenance moves every load on maintenanceRackID in the approved
// baseline onto another usable rack of the SAME thermal zone. It is pure and
// deterministic: largest load first, destination = lowest post-placement peak
// utilization. If any load cannot fit (power/airflow/units), Feasible is false
func PlanMaintenance(zones []model.ThermalZone, racks []model.Rack, loads []model.EquipmentLoad, baseline []dto.RackAssignment, maintenanceRackID uint) MaintenancePlan {
	zoneByID := map[uint]model.ThermalZone{}
	for _, zone := range zones {
		zoneByID[zone.ID] = zone
	}
	rackByID := map[uint]model.Rack{}
	for _, rack := range racks {
		rackByID[rack.ID] = rack
	}
	maintenanceRack, ok := rackByID[maintenanceRackID]
	if !ok {
		return MaintenancePlan{Feasible: false, Moves: []MaintenanceMove{}, Failures: []MaintenanceFailure{}}
	}
	targetZone, ok := zoneByID[maintenanceRack.ZoneID]
	if !ok {
		return MaintenancePlan{Feasible: false, Moves: []MaintenanceMove{}, Failures: []MaintenanceFailure{}}
	}

	loadByID := map[uint]model.EquipmentLoad{}
	for _, load := range loads {
		loadByID[load.ID] = load
	}
	state := map[uint]*maintenanceState{}
	for _, rack := range racks {
		state[rack.ID] = &maintenanceState{}
	}
	evacuated := []model.EquipmentLoad{}
	baselineByRack := map[uint][]dto.RackAssignment{}
	for _, assignment := range baseline {
		baselineByRack[assignment.RackID] = append(baselineByRack[assignment.RackID], assignment)
		if usage := state[assignment.RackID]; usage != nil {
			usage.powerKW += assignment.PowerKW
			usage.heatKW += assignment.HeatKW
			usage.airflowCFM += assignment.AirflowCFM
			usage.rackUnits += assignment.RackUnits
		}
		if assignment.RackID == maintenanceRackID {
			if load, exists := loadByID[assignment.LoadID]; exists {
				evacuated = append(evacuated, load)
			}
		}
	}
	sort.SliceStable(evacuated, func(i, j int) bool {
		if evacuated[i].PowerKW == evacuated[j].PowerKW {
			return evacuated[i].ID < evacuated[j].ID
		}
		return evacuated[i].PowerKW > evacuated[j].PowerKW
	})

	zoneRacks := []model.Rack{}
	for _, rack := range racks {
		if rack.ZoneID == targetZone.ID && rack.ID != maintenanceRackID {
			zoneRacks = append(zoneRacks, rack)
		}
	}
	sort.SliceStable(zoneRacks, func(i, j int) bool {
		if zoneRacks[i].RackCode == zoneRacks[j].RackCode {
			return zoneRacks[i].ID < zoneRacks[j].ID
		}
		return zoneRacks[i].RackCode < zoneRacks[j].RackCode
	})

	plan := MaintenancePlan{Moves: []MaintenanceMove{}, Failures: []MaintenanceFailure{}}
	if len(zoneRacks) == 0 && len(evacuated) > 0 {
		for _, load := range evacuated {
			plan.Failures = append(plan.Failures, MaintenanceFailure{LoadID: load.ID, LoadName: load.Name, Code: "MAINTENANCE_NO_SAME_ZONE_RACK", Message: "no other rack exists in the same thermal zone to receive the load"})
		}
		return plan
	}
	for _, load := range evacuated {
		candidates := []maintenanceCandidate{}
		for _, rack := range zoneRacks {
			peakUtilization, rejected := maintenanceCapacityShortfall(load, rack, state[rack.ID])
			if rejected != nil {
				plan.Failures = append(plan.Failures, *rejected)
				continue
			}
			candidates = append(candidates, maintenanceCandidate{rack: rack, score: peakUtilization})
		}
		if len(candidates) == 0 {
			continue // keep scanning remaining loads so evidence stays complete
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].score == candidates[j].score {
				return candidates[i].rack.RackCode < candidates[j].rack.RackCode
			}
			return candidates[i].score < candidates[j].score
		})
		selected := candidates[0]
		usage := state[selected.rack.ID]
		usage.powerKW += load.PowerKW
		usage.heatKW += load.HeatKW
		usage.airflowCFM += load.AirflowCFM
		usage.rackUnits += load.RackUnits
		plan.Moves = append(plan.Moves, MaintenanceMove{
			LoadID: load.ID, LoadName: load.Name, FromRackID: maintenanceRack.ID, FromRackCode: maintenanceRack.RackCode,
			ToRackID: selected.rack.ID, ToRackCode: selected.rack.RackCode, ZoneID: targetZone.ID, ZoneCode: targetZone.ZoneCode,
			PowerKW: load.PowerKW, HeatKW: load.HeatKW, AirflowCFM: load.AirflowCFM, RackUnits: load.RackUnits,
			PlacementScore: round2(100 - selected.score*100),
		})
	}
	plan.Feasible = len(plan.Moves) == len(evacuated)
	if !plan.Feasible {
		// Rejection: surface no assignments and keep the original layout; still
		// report the baseline thermal view and unchanged rack occupancy.
		zoneHeat := map[uint]float64{}
		for _, assignment := range baseline {
			zoneHeat[assignment.ZoneID] += assignment.HeatKW
			plan.TotalPowerKW += assignment.PowerKW
		}
		zoneResults, thermalViolations, peak := propagateThermal(zones, zoneHeat)
		plan.ZoneResults, plan.PeakTempC, plan.Score = zoneResults, peak, 0
		plan.Violations = append([]dto.ConstraintViolation{}, thermalViolations...)
		plan.RackViews = maintenanceRackViews(rackByID, zoneByID, baselineByRack, state, maintenanceRack, false)
		return plan
	}

	movedLoads := map[uint]bool{}
	plan.Assignments = []dto.RackAssignment{}
	for _, move := range plan.Moves {
		movedLoads[move.LoadID] = true
	}
	for _, assignment := range baseline {
		if assignment.RackID == maintenanceRack.ID && movedLoads[assignment.LoadID] {
			continue
		}
		plan.Assignments = append(plan.Assignments, assignment)
	}
	for _, move := range plan.Moves {
		plan.Assignments = append(plan.Assignments, dto.RackAssignment{
			LoadID: move.LoadID, LoadName: move.LoadName, RackID: move.ToRackID, RackCode: move.ToRackCode,
			ZoneID: move.ZoneID, ZoneCode: move.ZoneCode, PowerKW: move.PowerKW, HeatKW: move.HeatKW,
			AirflowCFM: move.AirflowCFM, RackUnits: move.RackUnits, PlacementScore: move.PlacementScore,
			Explanation: []string{"migrated within same thermal zone for rack maintenance"},
		})
	}
	sort.SliceStable(plan.Assignments, func(i, j int) bool {
		if plan.Assignments[i].RackCode == plan.Assignments[j].RackCode {
			return plan.Assignments[i].LoadID < plan.Assignments[j].LoadID
		}
		return plan.Assignments[i].RackCode < plan.Assignments[j].RackCode
	})
	zoneHeat, zonePower := map[uint]float64{}, map[uint]float64{}
	for _, assignment := range plan.Assignments {
		zoneHeat[assignment.ZoneID] += assignment.HeatKW
		zonePower[assignment.ZoneID] += assignment.PowerKW
		plan.TotalPowerKW += assignment.PowerKW
	}
	zoneResults, thermalViolations, peak := propagateThermal(zones, zoneHeat)
	plan.ZoneResults, plan.PeakTempC = zoneResults, peak
	plan.Violations = append([]dto.ConstraintViolation{}, thermalViolations...)
	plan.Violations = append(plan.Violations, validateFinalAssignments(racks, maintenanceUsageMap(state), zones, zonePower, plan.Assignments)...)
	plan.Score = scenarioScore(plan.Assignments, plan.ZoneResults, plan.Violations)
	plan.RackViews = maintenanceRackViews(rackByID, zoneByID, baselineByRack, state, maintenanceRack, true)
	return plan
}

// maintenanceCapacityShortfall returns post-placement peak utilization when
// the load fits, otherwise evidence for the first failing dimension.
func maintenanceCapacityShortfall(load model.EquipmentLoad, rack model.Rack, usage *maintenanceState) (float64, *MaintenanceFailure) {
	failure := func(code, message string, actual, limit float64) *MaintenanceFailure {
		return &MaintenanceFailure{LoadID: load.ID, LoadName: load.Name, RackID: rack.ID, RackCode: rack.RackCode, Code: code, Message: message, Actual: round2(actual), Limit: round2(limit)}
	}
	if !rack.IsUsable() {
		return 0, failure("RACK_UNAVAILABLE", "rack status does not accept a migrated load", 1, 0)
	}
	requiredPower := usage.powerKW + load.PowerKW
	if requiredPower > rack.PowerLimitKW {
		return 0, failure("RACK_POWER_LIMIT", "destination rack lacks power margin (kW)", requiredPower, rack.PowerLimitKW)
	}
	requiredAirflow := usage.airflowCFM + load.AirflowCFM
	if requiredAirflow > rack.AirflowLimitCFM {
		return 0, failure("RACK_AIRFLOW_LIMIT", "destination rack lacks airflow margin (CFM)", requiredAirflow, rack.AirflowLimitCFM)
	}
	requiredUnits := usage.rackUnits + load.RackUnits
	if requiredUnits > rack.RackUnits {
		return 0, failure("RACK_UNIT_LIMIT", "destination rack lacks rack-unit margin (U)", float64(requiredUnits), float64(rack.RackUnits))
	}
	return maxFloat(requiredPower/rack.PowerLimitKW, requiredAirflow/rack.AirflowLimitCFM, float64(requiredUnits)/float64(rack.RackUnits)), nil
}

func maintenanceRackViews(rackByID map[uint]model.Rack, zoneByID map[uint]model.ThermalZone, baselineByRack map[uint][]dto.RackAssignment, state map[uint]*maintenanceState, maintenanceRack model.Rack, applied bool) []MaintenanceRackView {
	touched := map[uint]bool{maintenanceRack.ID: true}
	for rackID, item := range state {
		if rackID == maintenanceRack.ID {
			continue
		}
		before := baselineState(baselineByRack[rackID])
		if applied && (item.powerKW != before.powerKW || item.airflowCFM != before.airflowCFM || item.rackUnits != before.rackUnits) {
			touched[rackID] = true
		}
	}
	views := make([]MaintenanceRackView, 0, len(touched))
	for rackID := range touched {
		rack, before := rackByID[rackID], baselineState(baselineByRack[rackID])
		view := MaintenanceRackView{
			RackID: rack.ID, RackCode: rack.RackCode, ZoneID: rack.ZoneID, ZoneCode: zoneByID[rack.ZoneID].ZoneCode,
			RackStatus: string(rack.RackStatus), BeforePowerKW: round2(before.powerKW), BeforeAirflowCFM: before.airflowCFM,
			BeforeRackUnits: before.rackUnits, PowerLimitKW: rack.PowerLimitKW, AirflowLimitCFM: rack.AirflowLimitCFM, RackUnitLimit: rack.RackUnits,
		}
		switch {
		case rackID == maintenanceRack.ID && applied:
			view.RackStatus = string(constants.RackMaintenance)
		case applied:
			item := state[rackID]
			view.AfterPowerKW, view.AfterAirflowCFM, view.AfterRackUnits = round2(item.powerKW), item.airflowCFM, item.rackUnits
		default:
			view.AfterPowerKW, view.AfterAirflowCFM, view.AfterRackUnits = view.BeforePowerKW, view.BeforeAirflowCFM, view.BeforeRackUnits
		}
		if rackID == maintenanceRack.ID && applied {
			view.AfterPowerKW, view.AfterAirflowCFM, view.AfterRackUnits = 0, 0, 0
		}
		views = append(views, view)
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].RackCode < views[j].RackCode })
	return views
}

func maintenanceUsageMap(state map[uint]*maintenanceState) map[uint]*rackUsage {
	usage := make(map[uint]*rackUsage, len(state))
	for rackID, item := range state {
		usage[rackID] = &rackUsage{powerKW: item.powerKW, heatKW: item.heatKW, airflowCFM: item.airflowCFM, rackUnits: item.rackUnits, groups: map[string]bool{}}
	}
	return usage
}

func baselineState(assignments []dto.RackAssignment) maintenanceState {
	state := maintenanceState{}
	for _, assignment := range assignments {
		state.powerKW += assignment.PowerKW
		state.heatKW += assignment.HeatKW
		state.airflowCFM += assignment.AirflowCFM
		state.rackUnits += assignment.RackUnits
	}
	return state
}
