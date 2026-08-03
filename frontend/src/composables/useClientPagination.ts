// frontend/src/composables/useClientPagination.ts
import { reactive } from 'vue'

export interface ClientPagination {
  page: number
  pageSize: number
  showSizePicker: boolean
  pageSizes: number[]
  onChange: (page: number) => void
  onUpdatePageSize: (pageSize: number) => void
}

/**
 * Reactive pagination object for client-side paging. The data is sliced
 * in-page, so changing page size resets to page 1.
 *
 * The change handlers live ON the pagination object (onChange /
 * onUpdatePageSize) rather than being wired as @update:page /
 * @update:page-size events, so the object works both with a raw NDataTable and
 * with ResponsiveDataTable (which drives a standalone mobile pager from the
 * same handlers instead of re-emitting table events).
 *
 * Shared by the small admin-configured lists (providers / models / model
 * candidates) that are fetched in full. The server-paged ApiKeyListPage
 * deliberately does NOT use this — its pagination is computed from the store
 * and triggers a reload, a different shape.
 */
export function useClientPagination(defaultPageSize = 20) {
  const pagination = reactive<ClientPagination>({
    page: 1,
    pageSize: defaultPageSize,
    showSizePicker: true,
    pageSizes: [10, 20, 50],
    onChange(page: number) {
      pagination.page = page
    },
    onUpdatePageSize(pageSize: number) {
      pagination.pageSize = pageSize
      pagination.page = 1
    },
  })
  return { pagination }
}
