import { defineStore } from 'pinia'
import * as apiKeysApi from '../api/apiKeys'
import type { APIKey, APIKeyPage, CreateAPIKeyInput, UpdateAPIKeyInput } from '../api/apiKeys'

interface ApiKeysState {
  list: APIKey[]
  total: number
  page: number
  pageSize: number
  query: string
  owner: string
  status: string
  // Admin-only owner-account filter (users.id); null = all accounts. The
  // backend pins member sessions to themselves regardless of this value.
  userId: number | null
  loading: boolean
  error: unknown | null
  // Monotonic token so a stale list response can't clobber a newer one if a
  // second fetchList() starts before the first resolves (same guard pattern
  // the models / providers stores use).
  lastFetchId: number
}

export const useApiKeysStore = defineStore('apiKeys', {
  state: (): ApiKeysState => ({
    list: [],
    total: 0,
    page: 1,
    pageSize: 20,
    query: '',
    owner: '',
    status: '',
    userId: null,
    loading: false,
    error: null,
    lastFetchId: 0,
  }),
  actions: {
    async fetchList() {
      const fetchId = ++this.lastFetchId
      this.loading = true
      this.error = null
      try {
        const res: APIKeyPage = await apiKeysApi.listAPIKeys({
          q: this.query,
          owner: this.owner,
          status: this.status,
          userId: this.userId,
          page: this.page,
          pageSize: this.pageSize,
        })
        // A newer fetchList() started while this one was in flight — its
        // result is authoritative, so leave state untouched.
        if (fetchId !== this.lastFetchId) return
        this.list = res.list
        this.total = res.total
      } catch (err) {
        if (fetchId !== this.lastFetchId) return
        this.error = err
        throw err
      } finally {
        if (fetchId === this.lastFetchId) this.loading = false
      }
    },
    // Applies the list filters at once and resets to the first page — the
    // search/reset buttons are the only thing that changes them.
    setFilters(f: { query: string; owner: string; status: string; userId: number | null }) {
      this.query = f.query
      this.owner = f.owner
      this.status = f.status
      this.userId = f.userId
      this.page = 1
    },
    setPage(page: number) {
      this.page = page
    },
    setPageSize(pageSize: number) {
      this.pageSize = pageSize
      this.page = 1
    },
    async create(input: CreateAPIKeyInput) {
      return apiKeysApi.createAPIKey(input)
    },
    async update(id: number, input: UpdateAPIKeyInput) {
      return apiKeysApi.updateAPIKey(id, input)
    },
    async revoke(id: number) {
      await apiKeysApi.revokeAPIKey(id)
    },
    // Fetch the full plaintext for the list-page copy button. Not stored in
    // state — it's a one-shot read handed back to the caller (same shape as
    // create's plaintext_key), since there's nothing to cache.
    async fetchPlaintext(id: number) {
      return apiKeysApi.getAPIKeyPlaintext(id)
    },
  },
})
