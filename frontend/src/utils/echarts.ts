// frontend/src/utils/echarts.ts
//
// Centralized ECharts registration. Importing this side-effect module once
// (from any component that renders a chart) registers the renderer, chart
// types and components we use project-wide. Keeping the `use([...])` list in
// one place avoids each chart component having to repeat it, and avoids
// accidentally tree-shaking away a component a sibling chart relies on.
//
// To add a new chart (e.g. PieChart) or component (e.g. TitleComponent):
//   1. add it to the import list below
//   2. add it to the `use([...])` array
// Nothing else in the codebase needs to change — every `<VChart>` already
// imports `./echarts` for registration.

import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import {
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  MarkLineComponent,
  TooltipComponent,
} from 'echarts/components'

use([
  CanvasRenderer,
  BarChart,
  LineChart,
  PieChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  DataZoomComponent,
  MarkLineComponent,
])

// Re-export VChart so callers can `import { VChart } from '@/utils/echarts'`
// and get both the component and the registration in one import.
export { default as VChart } from 'vue-echarts'

// Chart palette. ECharts canvas can't resolve CSS custom properties, so these
// mirror the design tokens (styles/tokens.less) as hex literals for use in
// chart option objects. Shared here so every chart draws from one definition
// instead of re-declaring the same hex in each component.
export const CHART_ACCENT = '#6467f2'
export const CHART_TEXT_MUTED = '#909399'
export const CHART_GRID_LINE = '#f0f0f3'
export const CHART_AXIS_LINE = '#e0e0e6'
