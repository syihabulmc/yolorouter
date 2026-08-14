// frontend/src/api/system.ts
//
// API client for the system version + update endpoints. The version
// response carries the build/runtime metadata shown on the About page and
// the resolved update status that drives the sidebar indicator. Mirrors the
// Go structs assembled in internal/handler/system_handler.go — when those
// change, update these interfaces in the same commit.

import { apiFetch } from './client'

export interface SystemVersion {
  version: string
  commit: string
  build_time: string
  go_version: string
  goos: string
  goarch: string
  db_driver: string
  update_mode: string
  uptime_seconds: number
  latest: string
  has_update: boolean
  release_url: string
  check_failed: boolean
}

// force=true marks an operator-initiated "check now": the server bypasses
// its result cache so a release published minutes ago is actually seen.
export function getSystemVersion(force = false): Promise<SystemVersion> {
  return apiFetch(force ? '/api/admin/system/version?force=1' : '/api/admin/system/version')
}

export interface SystemUpdateResult {
  status: 'updated' | 'up_to_date'
  target: string
}

// postSystemUpdate triggers the one-click in-place update. The server
// downloads and verifies the release before replying, which can take minutes
// on a slow link — the timeout must comfortably exceed the server's own
// download budget, so the default 30s is overridden here.
export function postSystemUpdate(): Promise<SystemUpdateResult> {
  return apiFetch('/api/admin/system/update', { method: 'POST', timeoutMs: 600_000 })
}
