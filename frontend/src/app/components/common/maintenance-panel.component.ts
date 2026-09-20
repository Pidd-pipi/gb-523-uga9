import { ChangeDetectionStrategy, Component, Input, computed, input, output, signal } from '@angular/core';
import { DecimalPipe } from '@angular/common';
import { MatButtonModule } from '@angular/material/button';
import { MatFormFieldModule } from '@angular/material/form-field';
import { MatInputModule } from '@angular/material/input';
import { LucideAngularModule } from 'lucide-angular';
import { Rack } from '../../../types/rack';
import { MaintenanceMigration, MaintenanceRejectionDetails } from '../../../types/maintenance';
import { ConstraintBadgeComponent } from './constraint-badge.component';

@Component({
  standalone: true,
  selector: 'app-maintenance-panel',
  imports: [DecimalPipe, MatButtonModule, MatFormFieldModule, MatInputModule, LucideAngularModule, ConstraintBadgeComponent],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section class="maintenance-panel">
      <header>
        <div>
          <h2><lucide-icon name="wrench" [size]="15" /> Rack maintenance migration</h2>
          <p>Evacuate the selected rack's planned loads onto another available/reserved rack in the same thermal zone. Power, airflow or rack-unit shortfalls reject the entire request.</p>
        </div>
        <button mat-icon-button type="button" (click)="closed.emit()"><lucide-icon name="x" [size]="16" /></button>
      </header>

      @if (result(); as migration) {
        <div class="outcome success">
          <app-constraint-badge severity="clear" label="Migration frozen as new draft" />
          <div class="outcome-meta">
            <strong>{{ migration.rack_code }}</strong>
            <span>{{ migration.zone_code }} · source #{{ migration.source_scenario_id }} → draft #{{ migration.draft_scenario_id }}</span>
          </div>
          <div class="moves">
            @for (move of migration.moves; track move.load_id) {
              <div class="move-row">
                <strong>{{ move.load_name }}</strong>
                <span class="path"><span class="rack from">{{ move.from_rack_code }}</span><lucide-icon name="arrow-right" [size]="13" /><span class="rack to">{{ move.to_rack_code }}</span></span>
                <small>{{ move.power_kw | number:'1.0-1' }} kW · {{ move.airflow_cfm | number:'1.0-0' }} CFM · {{ move.rack_units }}U</small>
              </div>
            }
          </div>
          <div class="rack-diff">
            <h3>Before → after occupancy</h3>
            @for (view of migration.rack_views; track view.rack_id) {
              <article class="diff-row" [class.maintained]="view.rack_status === 'maintenance'">
                <strong>{{ view.rack_code }}</strong>
                <span class="status-tag">{{ view.rack_status }}</span>
                <div class="dim"><label>Power</label><span [class.changed]="view.before_power_kw !== view.after_power_kw">{{ view.before_power_kw | number:'1.0-1' }} → {{ view.after_power_kw | number:'1.0-1' }} / {{ view.power_limit_kw | number:'1.0-0' }} kW</span></div>
                <div class="dim"><label>Airflow</label><span [class.changed]="view.before_airflow_cfm !== view.after_airflow_cfm">{{ view.before_airflow_cfm | number:'1.0-0' }} → {{ view.after_airflow_cfm | number:'1.0-0' }} / {{ view.airflow_limit_cfm | number:'1.0-0' }} CFM</span></div>
                <div class="dim"><label>U space</label><span [class.changed]="view.before_rack_units !== view.after_rack_units">{{ view.before_rack_units }} → {{ view.after_rack_units }} / {{ view.rack_unit_limit }} U</span></div>
              </article>
            }
          </div>
          <div class="panel-actions"><button mat-stroked-button type="button" (click)="reset()">Run another migration</button><button mat-flat-button color="primary" type="button" (click)="viewDraft.emit(migration.draft_scenario_id)">Open migration draft</button></div>
        </div>
      } @else if (rejection()) {
        @if (rejection(); as rejected) {
          <div class="outcome failure">
            <app-constraint-badge severity="critical" label="Entire migration rejected — layout unchanged" />
            <div class="outcome-meta">
              <strong>{{ rejected.rack_code }}</strong>
              <span>{{ rejected.zone_code }} · baseline #{{ rejected.source_scenario_id }}</span>
            </div>
            <h3>Failure reasons (per load × candidate rack)</h3>
            <div class="failure-list">
              @for (failure of groupedFailures(); track failure.key) {
                <div class="failure-row">
                  <app-constraint-badge severity="critical" [label]="failure.code" />
                  <div>
                    <p><strong>{{ failure.load_name }}</strong> cannot move to <strong>{{ failure.rack_code }}</strong></p>
                    <small>{{ failure.message }} · required {{ failure.actual | number:'1.0-1' }} / capacity {{ failure.limit | number:'1.0-1' }}</small>
                  </div>
                </div>
              }
            </div>
            <div class="panel-actions"><button mat-stroked-button type="button" (click)="reset()">Back to selection</button></div>
          </div>
        }
      } @else if (selectedRack()) {
        @if (selectedRack(); as rack) {
          <div class="form">
          <div class="selected-rack">
            <div><small>Selected rack</small><strong>{{ rack.rack_code }}</strong><span>{{ rack.zone_code }} · R{{ rack.row_index }} C{{ rack.column_index }}</span></div>
            <div><small>Capacity</small><strong>{{ rack.power_limit_kw | number:'1.0-0' }} kW</strong><span>{{ rack.airflow_limit_cfm | number:'1.0-0' }} CFM · {{ rack.rack_units }}U</span></div>
            <div><small>Status</small><strong [class.hot]="rack.rack_status === 'maintenance' || rack.rack_status === 'unavailable'">{{ rack.rack_status }}</strong><span>Only available or reserved can start</span></div>
          </div>
          <mat-form-field appearance="outline">
            <mat-label>Maintenance reason (optional)</mat-label>
            <textarea matInput rows="2" maxlength="500" [value]="reason()" (input)="reason.set($any($event.target).value)" placeholder="e.g. replace failing fan tray"></textarea>
          </mat-form-field>
          <p class="hint">The request uses the latest approved layout as the migration baseline. Loads on this rack are re-placed within the same thermal zone; the rack then switches to maintenance and the result is stored as a new readable draft scenario.</p>
          <div class="panel-actions">
            <button mat-button type="button" (click)="closed.emit()">Cancel</button>
            <button mat-flat-button color="primary" type="button" [disabled]="submitDisabled()" (click)="submit()">              {{ submitting() ? 'Migrating...' : 'Start maintenance' }}
            </button>
          </div>
        </div>
        }
      } @else {
        <div class="empty-hint">Pick an available or reserved rack in the rack map to start a maintenance migration.</div>
      }
    </section>
  `,
  styles: [`
    .maintenance-panel{margin-top:16px;background:#fff;border:1px solid #d8dde1;border-top:3px solid #3a6ea5}
    header{display:flex;align-items:flex-start;justify-content:space-between;gap:12px;padding:14px 16px;border-bottom:1px solid #e5e8ea}
    h2{margin:0;font-size:14px;display:flex;align-items:center;gap:6px}
    header p{margin:4px 0 0;color:#68717a;font-size:11px;line-height:1.5;max-width:820px}
    .outcome,.form,.empty-hint{padding:16px}
    .outcome-meta{display:flex;flex-direction:column;gap:2px;margin:10px 0}
    .outcome-meta strong{font-size:15px}.outcome-meta span{color:#68717a;font-size:11px}
    .moves{display:grid;gap:8px;margin-bottom:14px}
    .move-row{display:grid;grid-template-columns:minmax(0,1.6fr) auto minmax(0,1fr);gap:10px;align-items:center;padding:9px 11px;background:#f4f7fa;border-left:3px solid #3a6ea5;font-size:12px}
    .move-row small{color:#68717a;font-size:10px;text-align:right}
    .path{display:flex;align-items:center;gap:7px;color:#52606d}
    .rack{padding:2px 8px;border-radius:3px;font-size:10px;font-weight:700;letter-spacing:.04em}
    .rack.from{background:#fdecea;color:#b3261e}.rack.to{background:#e6f4ea;color:#1e6b3a}
    .rack-diff h3,.outcome h3{margin:0 0 8px;font-size:11px;text-transform:uppercase;color:#52606d}
    .diff-row{display:grid;grid-template-columns:auto auto 1fr 1fr 1fr;gap:12px;align-items:center;padding:10px;border:1px solid #e5e8ea;border-radius:4px;margin-bottom:8px}
    .diff-row.maintained{border-color:#cf3f2e;background:#fdf6f5}
    .status-tag{padding:2px 8px;background:#eef1f3;border-radius:3px;font:700 9px/1.4 monospace;letter-spacing:.05em}
    .maintained .status-tag{background:#fdecea;color:#b3261e}
    .dim label{display:block;font-size:8px;text-transform:uppercase;color:#8a949c}.dim span{font:600 10px/1.4 monospace}.dim span.changed{color:#1e6b3a}
    .maintained .dim span.changed{color:#b3261e}
    .panel-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:12px}
    .failure-list{display:grid;gap:8px;margin-bottom:10px}
    .failure-row{display:flex;gap:10px;align-items:flex-start;padding:10px;border:1px solid #f3c7c2;background:#fdf6f5;border-radius:4px}
    .failure-row p{margin:0 0 3px;font-size:12px}.failure-row small{color:#68717a;font-size:10px}
    .selected-rack{display:grid;grid-template-columns:repeat(3,1fr);gap:10px;margin-bottom:12px}
    .selected-rack>div{display:flex;flex-direction:column;gap:3px;padding:10px;background:#f4f7fa;border-left:3px solid #3a6ea5}
    .selected-rack small{text-transform:uppercase;font-size:8px;color:#8a949c;letter-spacing:.06em}
    .selected-rack span{color:#68717a;font-size:10px}
    .hot{color:#b3261e}.hint{color:#68717a;font-size:10px;line-height:1.5;margin:6px 0 0}
    .empty-hint{padding:28px 16px;color:#68717a;font-size:12px;text-align:center}
    @media(max-width:760px){.diff-row{grid-template-columns:1fr 1fr;}.selected-rack{grid-template-columns:1fr}.move-row{grid-template-columns:1fr}}
  `]
})
export class MaintenancePanelComponent {
  @Input() set rack(value: Rack | null) { this.selectedRack.set(value); }
  submitting = input(false);
  @Input() set migration(value: MaintenanceMigration | null) { this.result.set(value); }
  @Input() set rejected(value: MaintenanceRejectionDetails | null) { this.rejection.set(value); }
  readonly start = output<{rack: Rack; reason: string}>();
  readonly closed = output<void>();
  readonly viewDraft = output<number>();

  readonly selectedRack = signal<Rack | null>(null);
  readonly result = signal<MaintenanceMigration | null>(null);
  readonly rejection = signal<MaintenanceRejectionDetails | null>(null);
  readonly reason = signal('');

  readonly groupedFailures = computed(() => {
    const failures = this.rejection()?.failures ?? [];
    return failures.map((failure, index) => ({...failure, key: `${failure.load_id}-${failure.rack_id}-${failure.code}-${index}`}));
  });

  submitDisabled(): boolean {
    const rack = this.selectedRack();
    return this.submitting() || rack === null || rack.rack_status === 'maintenance' || rack.rack_status === 'unavailable';
  }

  submit(): void {
    const rack = this.selectedRack();
    if (!rack) {
      return;
    }
    this.start.emit({rack, reason: this.reason().trim()});
  }

  reset(): void {
    this.result.set(null);
    this.rejection.set(null);
    this.reason.set('');
  }
}
