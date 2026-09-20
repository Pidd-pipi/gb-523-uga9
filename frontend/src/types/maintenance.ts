import { RackAssignment, ZoneThermalResult, ConstraintViolation } from './scenario';

export interface MaintenanceFailure {
  load_id: number;
  load_name: string;
  rack_id: number;
  rack_code: string;
  code: string;
  message: string;
  actual: number;
  limit: number;
}

export interface MaintenanceMove {
  load_id: number;
  load_name: string;
  from_rack_id: number;
  from_rack_code: string;
  to_rack_id: number;
  to_rack_code: string;
  zone_id: number;
  zone_code: string;
  power_kw: number;
  heat_kw: number;
  airflow_cfm: number;
  rack_units: number;
  placement_score: number;
}

export interface MaintenanceRackView {
  rack_id: number;
  rack_code: string;
  zone_id: number;
  zone_code: string;
  rack_status: string;
  before_power_kw: number;
  after_power_kw: number;
  before_airflow_cfm: number;
  after_airflow_cfm: number;
  before_rack_units: number;
  after_rack_units: number;
  power_limit_kw: number;
  airflow_limit_cfm: number;
  rack_unit_limit: number;
}

export interface MaintenanceMigration {
  rack_id: number;
  rack_code: string;
  zone_id: number;
  zone_code: string;
  source_scenario_id: number;
  draft_scenario_id: number;
  draft_name: string;
  reason: string;
  moves: MaintenanceMove[];
  rack_views: MaintenanceRackView[];
  before: RackAssignment[];
  after: RackAssignment[];
  zone_results: ZoneThermalResult[];
  violations: ConstraintViolation[];
  failures: MaintenanceFailure[];
  total_power_kw: number;
  peak_temp_c: number;
  score: number;
}

export interface MaintenanceRejectionDetails {
  rack_id: number;
  rack_code: string;
  zone_id: number;
  zone_code: string;
  source_scenario_id: number;
  failures: MaintenanceFailure[];
  rack_views: MaintenanceRackView[];
  before: RackAssignment[];
}

export interface StartMaintenanceInput {
  source_scenario_id?: number;
  reason?: string;
}
