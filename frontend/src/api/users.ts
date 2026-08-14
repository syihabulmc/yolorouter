import { apiFetch } from './client'

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
