// frontend/src/api/systemSettings.ts
//
// API client for the global custom-system-prompt setting backing the
// Cost Optimization page. The GET returns the authoritative committed
// row (DB read, cache bypass) so the admin never sees a stale value;
// the PUT uses the returned version as an optimistic-lock token — a
// concurrent edit surfaces as errcode 11012 (HTTP 409), which the page
// treats as "reload and retry".

import { apiFetch } from './client'

export interface CustomSystemPromptSetting {
  enabled: boolean
  text: string
  version: number
}

export function getCustomSystemPrompt(): Promise<CustomSystemPromptSetting> {
  return apiFetch('/api/admin/system-settings/custom-system-prompt')
}

export function updateCustomSystemPrompt(payload: {
  enabled: boolean
  text: string
  version: number
}): Promise<CustomSystemPromptSetting> {
  return apiFetch('/api/admin/system-settings/custom-system-prompt', {
    method: 'PUT',
    body: JSON.stringify(payload),
  })
}
