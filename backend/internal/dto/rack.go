package dto

import (
	"errors"
	"fmt"
	"strings"

	"datacenter-thermal-capacity-planner/backend/internal/constants"
	"datacenter-thermal-capacity-planner/backend/internal/model"
)

type CreateRackRequest struct {
	ZoneID          uint                 `json:"zone_id" binding:"required"`
	RackCode        string               `json:"rack_code" binding:"required,min=2,max=32"`
	RowIndex        int                  `json:"row_index" binding:"gte=0,lte=100"`
	ColumnIndex     int                  `json:"column_index" binding:"gte=0,lte=100"`
	PowerLimitKW    float64              `json:"power_limit_kw" binding:"required,gt=0,lte=500"`
	AirflowLimitCFM float64              `json:"airflow_limit_cfm" binding:"required,gt=0,lte=100000"`
	RackUnits       int                  `json:"rack_units" binding:"required,gte=12,lte=60"`
	RackStatus      constants.RackStatus `json:"rack_status" binding:"required"`
}

type UpdateRackRequest struct {
	ZoneID          uint                 `json:"zone_id" binding:"required"`
	RowIndex        int                  `json:"row_index" binding:"gte=0,lte=100"`
	ColumnIndex     int                  `json:"column_index" binding:"gte=0,lte=100"`
	PowerLimitKW    float64              `json:"power_limit_kw" binding:"required,gt=0,lte=500"`
	AirflowLimitCFM float64              `json:"airflow_limit_cfm" binding:"required,gt=0,lte=100000"`
	RackUnits       int                  `json:"rack_units" binding:"required,gte=12,lte=60"`
	RackStatus      constants.RackStatus `json:"rack_status" binding:"required"`
	Version         uint                 `json:"version" binding:"required"`
}

type RackUtilization struct {
	PowerKW        float64 `json:"power_kw"`
	AirflowCFM     float64 `json:"airflow_cfm"`
	RackUnits      int     `json:"rack_units"`
	PowerPercent   float64 `json:"power_percent"`
	AirflowPercent float64 `json:"airflow_percent"`
	UnitsPercent   float64 `json:"units_percent"`
}

type RackResponse struct {
	ID              uint                 `json:"id"`
	ZoneID          uint                 `json:"zone_id"`
	ZoneCode        string               `json:"zone_code"`
	RackCode        string               `json:"rack_code"`
	RowIndex        int                  `json:"row_index"`
	ColumnIndex     int                  `json:"column_index"`
	PowerLimitKW    float64              `json:"power_limit_kw"`
	AirflowLimitCFM float64              `json:"airflow_limit_cfm"`
	RackUnits       int                  `json:"rack_units"`
	RackStatus      constants.RackStatus `json:"rack_status"`
	Version         uint                 `json:"version"`
	Utilization     RackUtilization      `json:"utilization"`
}

func (r CreateRackRequest) ValidateBusiness() error {
	if !constants.ValidRackStatus(r.RackStatus) {
		return fmt.Errorf("unsupported rack status %q", r.RackStatus)
	}
	if strings.TrimSpace(r.RackCode) == "" {
		return errors.New("rack code is required")
	}
	return nil
}

func (r UpdateRackRequest) ValidateBusiness() error {
	if !constants.ValidRackStatus(r.RackStatus) {
		return fmt.Errorf("unsupported rack status %q", r.RackStatus)
	}
	if r.Version == 0 {
		return errors.New("rack version is required")
	}
	return nil
}

func NewRack(req CreateRackRequest) (model.Rack, error) {
	if err := req.ValidateBusiness(); err != nil {
		return model.Rack{}, err
	}
	return model.Rack{
		ZoneID:          req.ZoneID,
		RackCode:        strings.ToUpper(strings.TrimSpace(req.RackCode)),
		RowIndex:        req.RowIndex,
		ColumnIndex:     req.ColumnIndex,
		PowerLimitKW:    req.PowerLimitKW,
		AirflowLimitCFM: req.AirflowLimitCFM,
		RackUnits:       req.RackUnits,
		RackStatus:      req.RackStatus,
		Version:         1,
	}, nil
}

type StartRackMaintenanceRequest struct {
	SourceScenarioID *uint  `json:"source_scenario_id"`
	Reason           string `json:"reason" binding:"max=500"`
}

type MaintenanceFailureResponse struct {
	LoadID   uint    `json:"load_id"`
	LoadName string  `json:"load_name"`
	RackID   uint    `json:"rack_id"`
	RackCode string  `json:"rack_code"`
	Code     string  `json:"code"`
	Message  string  `json:"message"`
	Actual   float64 `json:"actual"`
	Limit    float64 `json:"limit"`
}

type MaintenanceMoveResponse struct {
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

type MaintenanceRackViewResponse struct {
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

type MaintenanceMigrationResponse struct {
	RackID           uint                          `json:"rack_id"`
	RackCode         string                        `json:"rack_code"`
	ZoneID           uint                          `json:"zone_id"`
	ZoneCode         string                        `json:"zone_code"`
	SourceScenarioID uint                          `json:"source_scenario_id"`
	DraftScenarioID  uint                          `json:"draft_scenario_id"`
	DraftName        string                        `json:"draft_name"`
	Reason           string                        `json:"reason"`
	Moves            []MaintenanceMoveResponse     `json:"moves"`
	RackViews        []MaintenanceRackViewResponse `json:"rack_views"`
	Before           []RackAssignment              `json:"before"`
	After            []RackAssignment              `json:"after"`
	ZoneResults      []ZoneThermalResult           `json:"zone_results"`
	Violations       []ConstraintViolation         `json:"violations"`
	Failures         []MaintenanceFailureResponse  `json:"failures"`
	TotalPowerKW     float64                       `json:"total_power_kw"`
	PeakTempC        float64                       `json:"peak_temp_c"`
	Score            float64                       `json:"score"`
}

func (r StartRackMaintenanceRequest) ValidateBusiness() error {
	if r.SourceScenarioID != nil && *r.SourceScenarioID == 0 {
		return errors.New("source scenario id must be positive when provided")
	}
	return nil
}
