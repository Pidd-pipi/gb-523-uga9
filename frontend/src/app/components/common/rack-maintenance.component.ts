import { ChangeDetectionStrategy, Component, EventEmitter, Input, Output, computed, signal } from '@angular/core';
import { DecimalPipe } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { MatSelectModule } from '@angular/material/select';
import { MatSnackBar } from '@angular/material/snack-bar';
import { LucideAngularModule } from 'lucide-angular';
import { HttpErrorResponse } from '@angular/common/http';
import { finalize } from 'rxjs';
import { Rack } from '../../../types/rack';
import { Placement, MaintenanceFailureDetail, MaintenanceResult } from '../../../types/maintenance';
import { RackApi } from '../../api/rack.api';
import { useAuth } from '../../hooks/use-auth';

type Outcome =
  | {kind: 'success'; result: MaintenanceResult}
  | {kind: 'failure'; detail: MaintenanceFailureDetail; message: string}
  | null;

@Component({
  standalone: true,
  selector: 'app-rack-maintenance',
  imports: [DecimalPipe, FormsModule, MatButtonModule, MatFormFieldModule, MatInputModule, MatSelectModule, LucideAngularModule],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section class="maint-panel">
      <div class="panel-header">
        <div><h2>Rack maintenance migration</h2><span class="secondary">Move every planned load on a rack to same-zone racks with power, airflow and U-unit headroom before it enters maintenance.</span></div>
        <lucide-icon name="wrench" [size]="18" />
      </div>

      <div class="maint-form">
        <mat-form-field appearance="outline" subscriptSizing="dynamic">
          <mat-label>Available or reserved rack</mat-label>
          <mat-select [(value)]="selectedRackId">
            @for (rack of maintainableRacks(); track rack.id) {
              <mat-option [value]="rack.id">{{ rack.rack_code }} / {{ rack.zone_code }} / {{ hostedCount(rack.id) }} loads / {{ rack.rack_status }}</mat-option>
            }
          </mat-select>
        </mat-form-field>
        <mat-form-field appearance="outline" subscriptSizing="dynamic">
          <mat-label>Reason</mat-label>
          <input matInput [(ngModel)]="reason" maxlength="500" placeholder="e.g. fan tray replacement">
        </mat-form-field>
        <button mat-flat-button color="primary" (click)="start()" [disabled]="!selectedRackId() || busy() || !canPlan()">
          <lucide-icon name="play" [size]="15" /> {{ busy() ? 'Migrating...' : 'Start maintenance' }}
        </button>
      </div>

      @if (selectedRack(); as rack) {
        <div class="hosted-strip">
          <span class="secondary">Currently hosted on <strong>{{ rack.rack_code }}</strong> ({{ rack.zone_code }}):</span>
          @for (placement of placementsFor(rack.id); track placement.load_id) {
            <span class="hosted-chip"><lucide-icon name="cpu" [size]="12" />{{ placement.load_name }} <i>{{ placement.power_kw | number:'1.0-1' }} kW / {{ placement.airflow_cfm | number:'1.0-0' }} CFM / {{ placement.rack_units }}U</i></span>
          } @empty {<span class="secondary">No hosted loads; rack can enter maintenance directly.</span>}
        </div>
      }

      @if (outcome(); as outcome) {
        @if (outcome.kind === 'success') {
          <div class="outcome success">
            <header><lucide-icon name="check-circle-2" [size]="16" /><strong>{{ outcome.result.rack_code }} is now in maintenance</strong><span class="secondary">{{ outcome.result.moved_loads }} load(s) relocated &middot; draft {{ outcome.result.scenario.name }}</span></header>
            <div class="compare">
              <div class="compare-col">
                <h3>Before</h3>
                @for (view of outcome.result.before; track view.load_id + '-' + view.rack_id) {
                  <div class="line" [class.source]="view.rack_code === outcome.result.rack_code"><span>{{ view.load_name }}</span><strong>{{ view.rack_code }}</strong></div>
                }
              </div>
              <div class="compare-arrow"><lucide-icon name="arrow-right" [size]="18" /></div>
              <div class="compare-col">
                <h3>After</h3>
                @for (view of outcome.result.after; track view.load_id + '-' + view.rack_id) {
                  <div class="line" [class.dest]="isDest(outcome.result, view.rack_code)"><span>{{ view.load_name }}</span><strong>{{ view.rack_code }}</strong></div>
                }
              </div>
            </div>
            <div class="moves">
              @for (move of outcome.result.moves; track move.load_id) {
                <div class="move-line"><lucide-icon name="truck" [size]="13" /><strong>{{ move.load_name }}</strong><span>{{ move.from_rack_code }} &rarr; {{ move.to_rack_code }} ({{ move.zone_code }})</span><i>{{ move.power_kw | number:'1.0-1' }} kW / {{ move.airflow_cfm | number:'1.0-0' }} CFM / {{ move.rack_units }}U</i></div>
              }
            </div>
          </div>
        } @else {
          <div class="outcome failure">
            <header><lucide-icon name="alert-triangle" [size]="16" /><strong>Maintenance rejected &mdash; layout and rack status unchanged</strong><span class="secondary">{{ outcome.message }}</span></header>
            @for (failure of outcome.detail.load_failures; track failure.load_id) {
              <div class="load-failure">
                <div class="load-head"><lucide-icon name="cpu" [size]="13" /><strong>{{ failure.load_name }}</strong><span>{{ failure.power_kw | number:'1.0-1' }} kW / {{ failure.airflow_cfm | number:'1.0-0' }} CFM / {{ failure.rack_units }}U</span></div>
                <p class="reason">{{ failure.message }}</p>
                <table class="shortfall-table">
                  <thead><tr><th>Candidate rack</th><th>Short dimension</th><th>Required</th><th>Available</th></tr></thead>
                  <tbody>
                    @for (reject of failure.candidate_rejections; track reject.rack_id) {
                      @for (shortfall of reject.shortfalls; track shortfall.code) {
                        <tr><td>{{ reject.rack_code }}</td><td><code>{{ shortfall.code }}</code></td><td [class.over]="shortfall.required > shortfall.available">{{ shortfall.required | number:'1.0-1' }}</td><td>{{ shortfall.available | number:'1.0-1' }}</td></tr>
                      }
                    }
                  </tbody>
                </table>
              </div>
            }
          </div>
        }
      }
    </section>
  `,
  styles: [`
    .maint-panel{background:#fff;border:1px solid #d8dde1;border-top:3px solid #cf3f2e;margin-bottom:16px}
    .panel-header{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;padding:13px 16px;border-bottom:1px solid #d8dde1}
    .panel-header h2{margin:0 0 3px;font-size:14px;text-transform:uppercase}.panel-header .secondary{display:block}
    .secondary{color:#68717a;font-size:11px}
    .maint-form{display:grid;grid-template-columns:minmax(220px,1fr) minmax(220px,1.4fr) auto;gap:12px;align-items:center;padding:14px 16px}
    .maint-form button{height:52px}
    .hosted-strip{display:flex;flex-wrap:wrap;gap:8px;align-items:center;padding:0 16px 14px}
    .hosted-chip{display:inline-flex;align-items:center;gap:5px;padding:4px 9px;background:#f3f5f6;border:1px solid #d8dde1;border-left:2px solid #d79318;font-size:10px;color:#23282d}
    .hosted-chip i{font-style:normal;color:#68717a}
    .outcome{margin:0 16px 16px;border:1px solid;padding:13px 14px}
    .outcome.success{border-color:#3e8b68;background:#f3faf6}
    .outcome.failure{border-color:#cf3f2e;background:#fdf3f2}
    .outcome>header{display:flex;flex-direction:column;gap:3px;margin-bottom:11px}
    .outcome.success>header{color:#2c6a4f}.outcome.failure>header{color:#a52a1c}
    .outcome>header{flex-direction:row;align-items:center;gap:8px}.outcome>header .secondary{flex:1}
    .compare{display:grid;grid-template-columns:1fr 28px 1fr;gap:10px;margin-bottom:11px}
    .compare-col{background:#fff;border:1px solid #d8dde1;padding:9px}.compare-col h3{margin:0 0 7px;font-size:10px;text-transform:uppercase;color:#68717a}
    .compare-arrow{display:grid;place-items:center;color:#68717a}
    .line{display:flex;justify-content:space-between;gap:8px;padding:4px 6px;font-size:11px;border-bottom:1px solid #eef1f3}.line:last-child{border-bottom:0}.line.source{background:#fdf3f2;border-left:2px solid #cf3f2e}.line.dest{background:#eef8f2;border-left:2px solid #3e8b68}
    .moves{display:grid;gap:5px}.move-line{display:flex;align-items:center;gap:7px;font-size:11px;background:#fff;border:1px solid #d8dde1;padding:6px 9px}.move-line strong{min-width:150px}.move-line span{flex:1}.move-line i{font-style:normal;color:#68717a}
    .load-failure{background:#fff;border:1px solid #e2c4c0;padding:10px;margin-bottom:9px}.load-failure:last-child{margin-bottom:0}
    .load-head{display:flex;align-items:center;gap:7px;font-size:12px}.load-head span{margin-left:auto;color:#68717a;font-size:10px}
    .reason{margin:7px 0 9px;font-size:11px;color:#6a2d24}
    .shortfall-table{width:100%;border-collapse:collapse;font-size:10px}.shortfall-table th{text-align:left;color:#68717a;font-weight:600;padding:4px 6px;border-bottom:1px solid #d8dde1}.shortfall-table td{padding:4px 6px;border-bottom:1px solid #f0f1f2}.shortfall-table code{color:#a52a1c}.shortfall-table .over{color:#a52a1c;font-weight:700}
    @media(max-width:880px){.maint-form{grid-template-columns:1fr}.maint-form button{height:44px}.compare{grid-template-columns:1fr}.compare-arrow{transform:rotate(90deg)}}
  `]
})
export class RackMaintenanceComponent {
  readonly auth = useAuth();
  @Input() racks: Rack[] = [];
  @Input() placements: Placement[] = [];
  @Output() readonly changed = new EventEmitter<void>();

  readonly selectedRackId = signal<number | null>(null);
  readonly reason = '';
  readonly busy = signal(false);
  readonly outcome = signal<Outcome>(null);

  constructor(private readonly rackApi: RackApi, private readonly snack: MatSnackBar) {}

  readonly maintainableRacks = computed(() => this.racks.filter((rack) => rack.rack_status === 'available' || rack.rack_status === 'reserved'));
  readonly selectedRack = computed(() => this.racks.find((rack) => rack.id === this.selectedRackId()) ?? null);

  canPlan(): boolean { return this.auth.hasRole('planner', 'admin'); }
  hostedCount(rackId: number): number { return this.placements.filter((placement) => placement.rack_id === rackId).length; }
  placementsFor(rackId: number): Placement[] { return this.placements.filter((placement) => placement.rack_id === rackId); }

  isDest(outcome: MaintenanceResult, rackCode: string): boolean {
    return outcome.moves.some((move) => move.to_rack_code === rackCode);
  }

  start(): void {
    const rackId = this.selectedRackId();
    if (!rackId) return;
    this.busy.set(true);
    this.rackApi.startMaintenance(rackId, this.reason).pipe(finalize(() => this.busy.set(false))).subscribe({
      next: (result) => {
        this.outcome.set({kind: 'success', result});
        this.snack.open(`${result.rack_code} entered maintenance; ${result.moved_loads} load(s) migrated`, undefined, {duration: 3200});
        this.changed.emit();
      },
      error: (error: unknown) => {
        if (error instanceof HttpErrorResponse) {
          const details = error.error?.error?.details as MaintenanceFailureDetail | undefined;
          if (details && details.code === 'MAINTENANCE_CAPACITY_INSUFFICIENT') {
            this.outcome.set({kind: 'failure', detail: details, message: error.error?.error?.message ?? 'Capacity insufficient'});
            return;
          }
        }
        // Other errors (auth, conflict, validation) are surfaced by the global interceptor.
        this.outcome.set(null);
      },
    });
  }
}
