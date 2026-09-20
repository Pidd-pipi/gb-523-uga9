package planner

import (
	"sort"

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

// MaintenanceAlgorithmVersion identifies the deterministic rack-maintenance relocation.
const MaintenanceAlgorithmVersion = "rack-maintenance-v1"
const maintenanceEpsilon = 1e-9

// HostedEquipment pairs a load with the rack currently hosting it in the live layout.
type HostedEquipment struct {
	RackID uint
	Load   model.EquipmentLoad
}
type MaintenanceMove struct {
	LoadID       uint    `json:"load_id"`
	LoadName     string  `json:"load_name"`
	FromRackID   uint    `json:"from_rack_id"`
	FromRackCode string  `json:"from_rack_code"`
	ToRackID     uint    `json:"to_rack_id"`
	ToRackCode   string  `json:"to_rack_code"`
	ZoneID       uint    `json:"zone_id"`
	ZoneCode     string  `json:"zone_code"`
	PowerKW      float64 `json:"power_kw"`
	HeatKW       float64 `json:"heat_kw"`
	AirflowCFM   float64 `json:"airflow_cfm"`
	RackUnits    int     `json:"rack_units"`
}

// MaintenanceShortfall reports one capacity dimension blocking a destination rack.
type MaintenanceShortfall struct {
	Code      string  `json:"code"`
	Dimension string  `json:"dimension"`
	Message   string  `json:"message"`
	Required  float64 `json:"required"`
	Available float64 `json:"available"`
}
type MaintenanceCandidateReject struct {
	RackID     uint                   `json:"rack_id"`
	RackCode   string                 `json:"rack_code"`
	Shortfalls []MaintenanceShortfall `json:"shortfalls"`
}

// MaintenanceLoadFailure explains why one load could not be relocated in the zone.
type MaintenanceLoadFailure struct {
	LoadID              uint                         `json:"load_id"`
	LoadName            string                       `json:"load_name"`
	PowerKW             float64                      `json:"power_kw"`
	AirflowCFM          float64                      `json:"airflow_cfm"`
	RackUnits           int                          `json:"rack_units"`
	Message             string                       `json:"message"`
	CandidateRejections []MaintenanceCandidateReject `json:"candidate_rejections"`
}

// MaintenancePlan is the atomic relocation result for rack maintenance.
type MaintenancePlan struct {
	MaintenanceRackID   uint                     `json:"maintenance_rack_id"`
	MaintenanceRackCode string                   `json:"maintenance_rack_code"`
	ZoneID              uint                     `json:"zone_id"`
	ZoneCode            string                   `json:"zone_code"`
	Feasible            bool                     `json:"feasible"`
	Moves               []MaintenanceMove        `json:"moves"`
	LoadFailures        []MaintenanceLoadFailure `json:"load_failures"`
}
type maintenanceUsage struct {
	powerKW, airflowCFM float64
	rackUnits           int
}
type maintenanceUsageMap map[uint]*maintenanceUsage

func (m maintenanceUsageMap) add(rackID uint, load model.EquipmentLoad) {
	entry := m[rackID]
	if entry == nil {
		entry = &maintenanceUsage{}
		m[rackID] = entry
	}
	entry.powerKW += load.PowerKW
	entry.airflowCFM += load.AirflowCFM
	entry.rackUnits += load.RackUnits
}

func (m maintenanceUsageMap) shortfallsFor(rack model.Rack, load model.EquipmentLoad) []MaintenanceShortfall {
	used := m[rack.ID]
	if used == nil {
		used = &maintenanceUsage{}
	}
	powerNeed, airflowNeed := used.powerKW+load.PowerKW, used.airflowCFM+load.AirflowCFM
	unitsNeed := used.rackUnits + load.RackUnits
	out := []MaintenanceShortfall{}
	if powerNeed > rack.PowerLimitKW+maintenanceEpsilon {
		out = append(out, MaintenanceShortfall{Code: "MAINTENANCE_POWER_SHORTAGE", Dimension: "power_kw", Message: "rack " + rack.RackCode + " lacks power headroom", Required: powerNeed, Available: rack.PowerLimitKW})
	}
	if airflowNeed > rack.AirflowLimitCFM+maintenanceEpsilon {
		out = append(out, MaintenanceShortfall{Code: "MAINTENANCE_AIRFLOW_SHORTAGE", Dimension: "airflow_cfm", Message: "rack " + rack.RackCode + " lacks airflow headroom", Required: airflowNeed, Available: rack.AirflowLimitCFM})
	}
	if unitsNeed > rack.RackUnits {
		out = append(out, MaintenanceShortfall{Code: "MAINTENANCE_UNIT_SHORTAGE", Dimension: "rack_units", Message: "rack " + rack.RackCode + " lacks U-unit headroom", Required: float64(unitsNeed), Available: float64(rack.RackUnits)})
	}
	return out
}

// PlanMaintenance deterministically relocates the loads hosted by sourceRack onto other
// usable racks in its thermal zone. Any load without a fitting rack makes the whole plan
// infeasible so the caller rejects maintenance without a single mutation.
func PlanMaintenance(zones []model.ThermalZone, racks []model.Rack, hosted []HostedEquipment, sourceRackID uint) MaintenancePlan {
	zoneByID := make(map[uint]model.ThermalZone, len(zones))
	for _, zone := range zones {
		zoneByID[zone.ID] = zone
	}
	var source model.Rack
	found := false
	for _, rack := range racks {
		if rack.ID == sourceRackID {
			source, found = rack, true
			break
		}
	}
	plan := MaintenancePlan{MaintenanceRackID: sourceRackID, Moves: []MaintenanceMove{}, LoadFailures: []MaintenanceLoadFailure{}}
	if !found {
		return plan
	}
	plan.MaintenanceRackCode, plan.ZoneID = source.RackCode, source.ZoneID
	plan.ZoneCode = zoneByID[source.ZoneID].ZoneCode
	rackByID := make(map[uint]model.Rack, len(racks))
	for _, rack := range racks {
		rackByID[rack.ID] = rack
	}
	usage := maintenanceUsageMap{}
	moving := []HostedEquipment{}
	for _, entry := range hosted {
		if rack, ok := rackByID[entry.RackID]; ok {
			if rack.ID == source.ID {
				moving = append(moving, entry)
			} else if rack.ZoneID == source.ZoneID && rack.IsUsable() {
				usage.add(rack.ID, entry.Load)
			}
		}
	}
	destinations := []model.Rack{}
	for _, rack := range racks {
		if rack.ID != sourceRackID && rack.ZoneID == source.ZoneID && rack.IsUsable() {
			destinations = append(destinations, rack)
		}
	}
	sort.SliceStable(destinations, func(i, j int) bool { return destinations[i].RackCode < destinations[j].RackCode })
	sort.SliceStable(moving, func(i, j int) bool { return moving[i].Load.ID < moving[j].Load.ID })
	for _, hostedLoad := range moving {
		move, failure := placeMaintenanceLoad(hostedLoad, source, destinations, usage, zoneByID[source.ZoneID])
		if failure != nil {
			plan.LoadFailures = append(plan.LoadFailures, *failure)
			continue
		}
		usage.add(move.ToRackID, hostedLoad.Load)
		plan.Moves = append(plan.Moves, *move)
	}
	plan.Feasible = len(plan.LoadFailures) == 0
	return plan
}

func placeMaintenanceLoad(hosted HostedEquipment, source model.Rack, destinations []model.Rack, usage maintenanceUsageMap, zone model.ThermalZone) (*MaintenanceMove, *MaintenanceLoadFailure) {
	load := hosted.Load
	rejections := []MaintenanceCandidateReject{}
	for _, dest := range destinations {
		if shortfalls := usage.shortfallsFor(dest, load); len(shortfalls) > 0 {
			rejections = append(rejections, MaintenanceCandidateReject{RackID: dest.ID, RackCode: dest.RackCode, Shortfalls: shortfalls})
			continue
		}
		return &MaintenanceMove{LoadID: load.ID, LoadName: load.Name, FromRackID: source.ID, FromRackCode: source.RackCode, ToRackID: dest.ID, ToRackCode: dest.RackCode, ZoneID: dest.ZoneID, ZoneCode: zone.ZoneCode, PowerKW: load.PowerKW, HeatKW: load.HeatKW, AirflowCFM: load.AirflowCFM, RackUnits: load.RackUnits}, nil
	}
	message := "no usable rack in the same thermal zone had enough power, airflow and U-unit headroom"
	if len(destinations) == 0 {
		message = "no usable destination rack exists in the same thermal zone"
	}
	return nil, &MaintenanceLoadFailure{LoadID: load.ID, LoadName: load.Name, PowerKW: load.PowerKW, AirflowCFM: load.AirflowCFM, RackUnits: load.RackUnits, Message: message, CandidateRejections: rejections}
}
