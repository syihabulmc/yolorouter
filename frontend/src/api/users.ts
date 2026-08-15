import { apiFetch } from './client'

/**
 * Account status codes as stored by the backend (users.status). Reads
 * expose the numeric form; writes use the string enum ('enabled' |
 * 'disabled') — see updateUserStatus.
 */
export const USER_STATUS_ENABLED = 1

// Mirrors service.UserSummaryView. Backend wraps the array as
// { users: [...] }.
export interface UserSummary {
  id: number
  username: string
  display_name: string
  email: string
  role: string
  status: number
  is_local: boolean
  last_login_at: string | null
  created_at: string
  /** Login providers the account arrived through; empty for the local password account. */
  providers: string[]
  key_count: number
  spend_micros: number
}

export function listUsers(): Promise<{ users: UserSummary[] }> {
  return apiFetch<{ users: UserSummary[] }>('/api/admin/users')
}

// toUserOptions maps accounts to naive-ui <select> options. Kept here —
// next to the UserSummary type — so every user <select> (analytics filter,
// cost page scope) labels accounts the same way and can't drift.
export function toUserOptions(users: UserSummary[]): Array<{ label: string; value: number }> {
  return users.map((u) => ({
    label: u.display_name ? `${u.username} (${u.display_name})` : u.username,
    value: u.id,
  }))
}

export function updateUserStatus(id: number, status: 'enabled' | 'disabled'): Promise<null> {
  return apiFetch<null>(`/api/admin/users/${id}/status`, {
    method: 'PATCH',
    body: JSON.stringify({ status }),
  })
}

export function updateUserRole(id: number, role: 'admin' | 'member'): Promise<null> {
  return apiFetch<null>(`/api/admin/users/${id}/role`, {
    method: 'PATCH',
    body: JSON.stringify({ role }),
  })
}
