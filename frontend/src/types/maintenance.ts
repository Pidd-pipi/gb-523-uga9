export interface Placement {
  id: number;
  rack_id: number;
  rack_code: string;
  zone_id: number;
  zone_code: string;
  load_id: number;
  load_name: string;
  power_kw: number;
  heat_kw: number;
  airflow_cfm: number;
  rack_units: number;
  source: string;
  created_at: string;
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
}

export interface MaintenancePlacementView {
  load_id: number;
  load_name: string;
  rack_id: number;
  rack_code: string;
  zone_id: number;
  zone_code: string;
  power_kw: number;
  airflow_cfm: number;
  rack_units: number;
}

export interface MaintenanceSnapshot {
  kind: 'rack_maintenance';
  algorithm_version: string;
  maintenance_rack_id: number;
  maintenance_rack_code: string;
  zone_id: number;
  zone_code: string;
  reason: string;
  before: MaintenancePlacementView[];
  after: MaintenancePlacementView[];
  moves: MaintenanceMove[];
}

export interface MaintenanceShortfall {
  code: string;
  dimension: string;
  message: string;
  required: number;
  available: number;
}

export interface MaintenanceCandidateReject {
  rack_id: number;
  rack_code: string;
  shortfalls: MaintenanceShortfall[];
}

export interface MaintenanceLoadFailure {
  load_id: number;
  load_name: string;
  power_kw: number;
  airflow_cfm: number;
  rack_units: number;
  message: string;
  candidate_rejections: MaintenanceCandidateReject[];
}

export interface MaintenanceFailureDetail {
  code: string;
  maintenance_rack_id: number;
  maintenance_rack_code: string;
  zone_id: number;
  zone_code: string;
  load_failures: MaintenanceLoadFailure[];
}

export interface MaintenanceResult {
  rack_id: number;
  rack_code: string;
  zone_id: number;
  zone_code: string;
  rack_status: 'maintenance';
  rack_version: number;
  moved_loads: number;
  moves: MaintenanceMove[];
  before: MaintenancePlacementView[];
  after: MaintenancePlacementView[];
  scenario: import('./scenario').LayoutScenario;
}
