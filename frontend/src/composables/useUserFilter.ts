// useUserFilter is the per-account scope every admin stats page carries:
// the selected account id (seeded from ?user_id= so drill-downs keep the
// active scope), the account options for the select, and a change handler
// that re-runs the page's own reload. Extracted because the same
// seed-parse + ref + handler block had grown five near-identical copies.
// The backend pins member sessions to themselves regardless of this value,
// so pages only need to gate the CONTROL's visibility on the admin role,
// not the state.
import { ref } from 'vue'
import { useRoute, type RouteLocationRaw } from 'vue-router'
import { useUserOptions } from './useUserOptions'

export function useUserFilter(onChange: () => void) {
  const route = useRoute()
  const { userOptions, loadUserOptions } = useUserOptions()
  const seeded = Number(route.query.user_id)
  const selectedUserId = ref<number | null>(Number.isInteger(seeded) && seeded > 0 ? seeded : null)

  function onUserChange(v: number | null) {
    selectedUserId.value = v
    onChange()
  }

  // withUserQuery threads the selected account into a drill-down route so
  // the target page (which seeds itself from ?user_id=) opens under the
  // same scope the admin is looking at.
  function withUserQuery(loc: RouteLocationRaw): RouteLocationRaw {
    if (selectedUserId.value == null) return loc
    const obj = typeof loc === 'string' ? { path: loc } : (loc as { path?: string; query?: Record<string, unknown> })
    return { ...obj, query: { ...(obj.query ?? {}), user_id: String(selectedUserId.value) } }
  }

  // userQueryParam is the same scope as a plain query fragment, for route
  // helpers that assemble their query from records (logsRouteWithRange).
  function userQueryParam(): Record<string, string> {
    return selectedUserId.value != null ? { user_id: String(selectedUserId.value) } : {}
  }

  return { selectedUserId, userOptions, loadUserOptions, onUserChange, withUserQuery, userQueryParam }
}
