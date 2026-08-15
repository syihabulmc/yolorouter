// useUserOptions loads the account directory into naive-ui <select> options.
// Every admin page with a per-account filter (dashboard, analytics, costs,
// request logs, API keys) needs the same ref + fetch + error-toast shape;
// keeping it here means they all label accounts identically (via
// toUserOptions) and fail identically. The endpoint is admin-only — callers
// gate the load on the admin role, same as the filter control itself.
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, type SelectOption } from 'naive-ui'
import { displayMessage } from '../api/client'
import { listUsers, toUserOptions } from '../api/users'

export function useUserOptions() {
  const { t } = useI18n()
  const message = useMessage()
  const userOptions = ref<SelectOption[]>([])

  // Load failures toast and leave the options empty — an empty account
  // dropdown degrades to "no filter", which is the correct fallback for a
  // filter control.
  async function loadUserOptions() {
    try {
      userOptions.value = toUserOptions((await listUsers()).users)
    } catch (err) {
      message.error(displayMessage(err, t))
    }
  }

  return { userOptions, loadUserOptions }
}
