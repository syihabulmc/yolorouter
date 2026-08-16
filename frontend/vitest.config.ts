import { defineConfig } from 'vitest/config'

// Explicit test discovery for the build gate: `npm run build` runs
// `vitest run` before the type check and bundle, and this include pins
// what that gate covers.
export default defineConfig({
  test: {
    include: ['src/**/*.test.ts'],
  },
})
