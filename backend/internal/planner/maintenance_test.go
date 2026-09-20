package planner

import (
	"testing"

	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/dto"
	"datacenter-thermal-capacity-planner/backend/internal/model"
)

func TestPlanMaintenance(t *testing.T) {
	zoneA := model.ThermalZone{ID: 1, ZoneCode: "TZ-A", CoolingCapacityKW: 120, SupplyTempC: 18, MaxReturnTempC: 32, AdjacencyJSON: `{}`, ZoneStatus: "active"}
	zoneB := model.ThermalZone{ID: 2, ZoneCode: "TZ-B", CoolingCapacityKW: 120, SupplyTempC: 18, MaxReturnTempC: 32, AdjacencyJSON: `{}`, ZoneStatus: "active"}

	loadOnRack := func(id uint, power, heat, airflow float64, units int) model.EquipmentLoad {
		return model.EquipmentLoad{ID: id, Name: "load", PowerKW: power, HeatKW: heat, AirflowCFM: airflow, RackUnits: units, RedundancyGroup: "G", LoadStatus: "ready"}
	}
	assign := func(loadID uint, loadName string, rackID uint, rackCode string, zoneID uint, zoneCode string, power, heat, airflow float64, units int) dto.RackAssignment {
		return dto.RackAssignment{LoadID: loadID, LoadName: loadName, RackID: rackID, RackCode: rackCode, ZoneID: zoneID, ZoneCode: zoneCode, PowerKW: power, HeatKW: heat, AirflowCFM: airflow, RackUnits: units}
	}

	tests := []struct {
		name            string
		racks           []model.Rack
		loads           []model.EquipmentLoad
		baseline        []dto.RackAssignment
		maintenanceRack uint
		feasible        bool
		moves           int
		failureCodes    map[string]int
		evacuatedPower  float64
	}{
		{
			name: "single load moves to same zone rack with margin",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
			},
			loads:           []model.EquipmentLoad{loadOnRack(10, 8, 7.5, 2000, 8)},
			baseline:        []dto.RackAssignment{assign(10, "load", 1, "A-01", 1, "TZ-A", 8, 7.5, 2000, 8)},
			maintenanceRack: 1, feasible: true, moves: 1,
		},
		{
			name: "reserved rack in same zone can receive migration",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackReserved},
			},
			loads:           []model.EquipmentLoad{loadOnRack(10, 8, 7.5, 2000, 8)},
			baseline:        []dto.RackAssignment{assign(10, "load", 1, "A-01", 1, "TZ-A", 8, 7.5, 2000, 8)},
			maintenanceRack: 1, feasible: true, moves: 1,
		},
		{
			name: "other zone rack is never used even when same zone lacks margin",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 2, RackCode: "B-01", PowerLimitKW: 100, AirflowLimitCFM: 99999, RackUnits: 60, RackStatus: constants.RackAvailable},
			},
			loads:           []model.EquipmentLoad{loadOnRack(10, 30, 28, 9000, 50)},
			baseline:        []dto.RackAssignment{assign(10, "load", 1, "A-01", 1, "TZ-A", 30, 28, 9000, 50)},
			maintenanceRack: 1,
			feasible:        false,
			failureCodes:    map[string]int{"MAINTENANCE_NO_SAME_ZONE_RACK": 1},
		},
		{
			name: "same zone rack reports power shortfall and other zone rack is ignored",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 10, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 3, ZoneID: 2, RackCode: "B-01", PowerLimitKW: 100, AirflowLimitCFM: 99999, RackUnits: 60, RackStatus: constants.RackAvailable},
			},
			loads:           []model.EquipmentLoad{loadOnRack(10, 30, 28, 2000, 8)},
			baseline:        []dto.RackAssignment{assign(10, "load", 1, "A-01", 1, "TZ-A", 30, 28, 2000, 8)},
			maintenanceRack: 1,
			feasible:        false,
			failureCodes:    map[string]int{"RACK_POWER_LIMIT": 1},
		},
		{
			name: "unavailable same zone rack is reported and whole migration rejected",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackUnavailable},
			},
			loads:           []model.EquipmentLoad{loadOnRack(10, 8, 7.5, 2000, 8)},
			baseline:        []dto.RackAssignment{assign(10, "load", 1, "A-01", 1, "TZ-A", 8, 7.5, 2000, 8)},
			maintenanceRack: 1,
			feasible:        false,
			failureCodes:    map[string]int{"RACK_UNAVAILABLE": 1},
		},
		{
			name: "two loads balanced across same zone racks",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 3, ZoneID: 1, RackCode: "A-03", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
			},
			loads: []model.EquipmentLoad{
				loadOnRack(10, 15, 14, 3000, 10),
				loadOnRack(11, 15, 14, 3000, 10),
			},
			baseline: []dto.RackAssignment{
				assign(10, "load-a", 1, "A-01", 1, "TZ-A", 15, 14, 3000, 10),
				assign(11, "load-b", 1, "A-01", 1, "TZ-A", 15, 14, 3000, 10),
			},
			maintenanceRack: 1,
			feasible:        true,
			moves:           2,
			evacuatedPower:  30,
		},
		{
			name: "existing destination occupancy is respected and airflow shortfall rejects all",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 40, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
			},
			loads: []model.EquipmentLoad{
				loadOnRack(10, 5, 4.5, 3000, 4),
				loadOnRack(11, 5, 4.5, 3000, 4),
			},
			baseline: []dto.RackAssignment{
				assign(9, "resident", 2, "A-02", 1, "TZ-A", 5, 4.5, 2000, 4),
				assign(10, "move-a", 1, "A-01", 1, "TZ-A", 5, 4.5, 3000, 4),
				assign(11, "move-b", 1, "A-01", 1, "TZ-A", 5, 4.5, 3000, 4),
			},
			maintenanceRack: 1,
			feasible:        false,
			// Neither load can fit once the resident airflow is accounted for;
			// each load produces its own evidence entry.
			failureCodes: map[string]int{"RACK_AIRFLOW_LIMIT": 2},
		},
		{
			name: "rack unit shortfall rejects",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 12, RackStatus: constants.RackAvailable},
			},
			loads:           []model.EquipmentLoad{loadOnRack(10, 5, 4.5, 1000, 20)},
			baseline:        []dto.RackAssignment{assign(10, "load", 1, "A-01", 1, "TZ-A", 5, 4.5, 1000, 20)},
			maintenanceRack: 1,
			feasible:        false,
			failureCodes:    map[string]int{"RACK_UNIT_LIMIT": 1},
		},
		{
			name: "empty maintenance rack succeeds with zero moves",
			racks: []model.Rack{
				{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
				{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 20, AirflowLimitCFM: 4000, RackUnits: 42, RackStatus: constants.RackAvailable},
			},
			loads:           []model.EquipmentLoad{loadOnRack(10, 8, 7.5, 2000, 8)},
			baseline:        []dto.RackAssignment{assign(10, "load", 2, "A-02", 1, "TZ-A", 8, 7.5, 2000, 8)},
			maintenanceRack: 1,
			feasible:        true,
			moves:           0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanMaintenance([]model.ThermalZone{zoneA, zoneB}, tt.racks, tt.loads, tt.baseline, tt.maintenanceRack)
			if plan.Feasible != tt.feasible {
				t.Fatalf("feasible = %v, want %v; failures=%+v", plan.Feasible, tt.feasible, plan.Failures)
			}
			if len(plan.Moves) != tt.moves {
				t.Fatalf("moves = %d, want %d", len(plan.Moves), tt.moves)
			}
			counts := map[string]int{}
			for _, failure := range plan.Failures {
				counts[failure.Code]++
			}
			for code, want := range tt.failureCodes {
				if counts[code] != want {
					t.Fatalf("failure code %s count = %d, want %d; all=%+v", code, counts[code], want, plan.Failures)
				}
			}
			if tt.feasible {
				for _, move := range plan.Moves {
					if move.FromRackID == move.ToRackID {
						t.Fatalf("load %d stayed on maintenance rack", move.LoadID)
					}
					if move.ZoneID != 1 {
						t.Fatalf("load %d migrated out of thermal zone", move.LoadID)
					}
				}
				if tt.evacuatedPower > 0 {
					total := 0.0
					for _, move := range plan.Moves {
						total += move.PowerKW
					}
					if total != tt.evacuatedPower {
						t.Fatalf("evacuated power = %.1f, want %.1f", total, tt.evacuatedPower)
					}
				}
				// Maintenance rack must be empty in the post-migration layout.
				for _, assignment := range plan.Assignments {
					if assignment.RackID == tt.maintenanceRack {
						t.Fatalf("maintenance rack still carries load %d", assignment.LoadID)
					}
				}
				// Rack views must show the maintenance rack drained.
				for _, view := range plan.RackViews {
					if view.RackID == tt.maintenanceRack {
						if view.AfterPowerKW != 0 || view.AfterAirflowCFM != 0 || view.AfterRackUnits != 0 {
							t.Fatalf("maintenance rack not drained: %+v", view)
						}
						if view.RackStatus != string(constants.RackMaintenance) {
							t.Fatalf("rack status = %s, want maintenance", view.RackStatus)
						}
					}
				}
			} else if len(tt.failureCodes) > 0 {
				if len(plan.Failures) == 0 {
					t.Fatal("expected rejection evidence, got none")
				}
				if len(plan.Assignments) != 0 {
					t.Fatalf("infeasible plan must not surface assignments: %+v", plan.Assignments)
				}
			}
		})
	}
}

func TestPlanMaintenanceDeterministic(t *testing.T) {
	zones := []model.ThermalZone{
		{ID: 1, ZoneCode: "TZ-A", CoolingCapacityKW: 120, SupplyTempC: 18, MaxReturnTempC: 32, AdjacencyJSON: `{}`, ZoneStatus: "active"},
	}
	racks := []model.Rack{
		{ID: 1, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
		{ID: 2, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
		{ID: 3, ZoneID: 1, RackCode: "A-03", PowerLimitKW: 40, AirflowLimitCFM: 8000, RackUnits: 42, RackStatus: constants.RackAvailable},
	}
	loads := []model.EquipmentLoad{
		{ID: 10, Name: "a", PowerKW: 10, HeatKW: 9, AirflowCFM: 2000, RackUnits: 8, LoadStatus: "ready"},
		{ID: 11, Name: "b", PowerKW: 10, HeatKW: 9, AirflowCFM: 2000, RackUnits: 8, LoadStatus: "ready"},
	}
	baseline := []dto.RackAssignment{
		{LoadID: 10, RackID: 1, RackCode: "A-01", ZoneID: 1, ZoneCode: "TZ-A", PowerKW: 10, HeatKW: 9, AirflowCFM: 2000, RackUnits: 8},
		{LoadID: 11, RackID: 1, RackCode: "A-01", ZoneID: 1, ZoneCode: "TZ-A", PowerKW: 10, HeatKW: 9, AirflowCFM: 2000, RackUnits: 8},
	}
	first := PlanMaintenance(zones, racks, loads, baseline, 1)
	second := PlanMaintenance(zones, racks, loads, baseline, 1)
	if len(first.Moves) != len(second.Moves) {
		t.Fatal("move count differs between runs")
	}
	for i := range first.Moves {
		if first.Moves[i].ToRackID != second.Moves[i].ToRackID {
			t.Fatalf("move %d destination changed: %d vs %d", i, first.Moves[i].ToRackID, second.Moves[i].ToRackID)
		}
	}
}
