import { Injectable, inject } from '@angular/core';
import { Observable } from 'rxjs';
import { ApiClient } from './api-client';
import { MaintenanceMigration, StartMaintenanceInput } from '../../types/maintenance';

@Injectable({providedIn: 'root'})
export class MaintenanceApi {
  private readonly api = inject(ApiClient);
  start(rackId: number, input: StartMaintenanceInput = {}): Observable<MaintenanceMigration> {
    return this.api.post<MaintenanceMigration>(`/racks/${rackId}/maintenance`, input);
  }
}
