package planner

import (
	"testing"

	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/model"
)

func maintenanceFixture2() ([]model.ThermalZone, []model.Rack) {
	zones := []model.ThermalZone{
		{ID: 1, ZoneCode: "TZ-A", CoolingCapacityKW: 200, ZoneStatus: "active"},
		{ID: 2, ZoneCode: "TZ-B", CoolingCapacityKW: 200, ZoneStatus: "active"},
	}
	racks := []model.Rack{
		{ID: 10, ZoneID: 1, RackCode: "A-01", PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackAvailable},
		{ID: 11, ZoneID: 1, RackCode: "A-02", PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackAvailable},
		{ID: 12, ZoneID: 1, RackCode: "A-03", PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackReserved},
		{ID: 13, ZoneID: 1, RackCode: "A-04", PowerLimitKW: 20, AirflowLimitCFM: 6000, RackUnits: 42, RackStatus: constants.RackUnavailable},
		{ID: 20, ZoneID: 2, RackCode: "B-01", PowerLimitKW: 30, AirflowLimitCFM: 9000, RackUnits: 48, RackStatus: constants.RackAvailable},
	}
	return zones, racks
}

func mLoad(id uint, power, airflow float64, units int) model.EquipmentLoad {
	return model.EquipmentLoad{ID: id, Name: "load", PowerKW: power, HeatKW: power * 0.95, AirflowCFM: airflow, RackUnits: units, RedundancyGroup: "G", LoadStatus: "ready"}
}

func TestPlanMaintenance(t *testing.T) {
	zones, racks := maintenanceFixture2()
	tests := []struct {
		name         string
		source       uint
		hosted       []HostedEquipment
		wantFeasible bool
		wantMoves    int
		wantCode     string
	}{
		{name: "empty rack is feasible", source: 10, hosted: nil, wantFeasible: true, wantMoves: 0},
		{name: "single load relocates", source: 10, hosted: []HostedEquipment{{RackID: 10, Load: mLoad(1, 8, 2000, 6)}}, wantFeasible: true, wantMoves: 1},
		{name: "multiple loads pack", source: 10, hosted: []HostedEquipment{{RackID: 10, Load: mLoad(2, 12, 3000, 10)}, {RackID: 10, Load: mLoad(1, 12, 3000, 10)}}, wantFeasible: true, wantMoves: 2},
		{name: "occupied rack pushes to reserved rack", source: 10, hosted: []HostedEquipment{{RackID: 11, Load: mLoad(9, 15, 1000, 4)}, {RackID: 10, Load: mLoad(1, 12, 2000, 4)}}, wantFeasible: true, wantMoves: 1},
		{name: "power shortage rejects", source: 10, hosted: []HostedEquipment{{RackID: 11, Load: mLoad(8, 15, 1000, 4)}, {RackID: 12, Load: mLoad(7, 15, 1000, 4)}, {RackID: 10, Load: mLoad(1, 12, 2000, 4)}}, wantFeasible: false, wantCode: "MAINTENANCE_POWER_SHORTAGE"},
		{name: "airflow shortage rejects", source: 10, hosted: []HostedEquipment{{RackID: 11, Load: mLoad(8, 1, 4000, 4)}, {RackID: 12, Load: mLoad(7, 1, 4000, 4)}, {RackID: 10, Load: mLoad(1, 1, 4000, 4)}}, wantFeasible: false, wantCode: "MAINTENANCE_AIRFLOW_SHORTAGE"},
		{name: "u-unit shortage rejects", source: 10, hosted: []HostedEquipment{{RackID: 11, Load: mLoad(8, 1, 100, 30)}, {RackID: 12, Load: mLoad(7, 1, 100, 30)}, {RackID: 10, Load: mLoad(1, 1, 100, 30)}}, wantFeasible: false, wantCode: "MAINTENANCE_UNIT_SHORTAGE"},
		{name: "other zone never a destination", source: 20, hosted: []HostedEquipment{{RackID: 20, Load: mLoad(1, 5, 1000, 4)}}, wantFeasible: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanMaintenance(zones, racks, tt.hosted, tt.source)
			if plan.Feasible != tt.wantFeasible {
				t.Fatalf("feasible=%v want %v failures=%+v", plan.Feasible, tt.wantFeasible, plan.LoadFailures)
			}
			if len(plan.Moves) != tt.wantMoves {
				t.Fatalf("moves=%d want %d", len(plan.Moves), tt.wantMoves)
			}
			if !tt.wantFeasible && tt.wantCode != "" {
				if len(plan.LoadFailures) == 0 || len(plan.LoadFailures[0].CandidateRejections) == 0 {
					t.Fatalf("expected evidence, got %+v", plan.LoadFailures)
				}
				codes := map[string]bool{}
				for _, r := range plan.LoadFailures[0].CandidateRejections {
					for _, s := range r.Shortfalls {
						codes[s.Code] = true
					}
				}
				if !codes[tt.wantCode] {
					t.Fatalf("expected %s got %v", tt.wantCode, codes)
				}
			}
		})
	}
}

func TestPlanMaintenanceDeterministic(t *testing.T) {
	zones, racks := maintenanceFixture2()
	hosted := []HostedEquipment{{RackID: 10, Load: mLoad(3, 6, 1500, 8)}, {RackID: 10, Load: mLoad(1, 6, 1500, 8)}, {RackID: 10, Load: mLoad(2, 6, 1500, 8)}}
	first, second := PlanMaintenance(zones, racks, hosted, 10), PlanMaintenance(zones, racks, hosted, 10)
	if len(first.Moves) != 3 {
		t.Fatalf("expected 3 moves failures=%+v", first.LoadFailures)
	}
	for i := range first.Moves {
		if first.Moves[i].ToRackID != second.Moves[i].ToRackID {
			t.Fatalf("non-deterministic %+v %+v", first.Moves, second.Moves)
		}
	}
	if first.Moves[0].LoadID != 1 || first.Moves[0].ToRackCode != "A-02" {
		t.Fatalf("unexpected first move %+v", first.Moves[0])
	}
}

func TestPlanMaintenancePacksBeforeSpreading(t *testing.T) {
	zones, racks := maintenanceFixture2()
	hosted := []HostedEquipment{{RackID: 10, Load: mLoad(1, 9, 2000, 6)}, {RackID: 10, Load: mLoad(2, 9, 2000, 6)}}
	plan := PlanMaintenance(zones, racks, hosted, 10)
	if !plan.Feasible {
		t.Fatalf("expected feasible %+v", plan.LoadFailures)
	}
	if plan.Moves[0].ToRackID != plan.Moves[1].ToRackID {
		t.Fatalf("expected packing on one rack %+v", plan.Moves)
	}
}
