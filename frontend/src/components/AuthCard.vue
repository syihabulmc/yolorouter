<!-- Shared shell for the setup/login pages (frontend/src/views/auth/*.vue):
     a split layout with a dark brand panel on the left (product identity)
     and the page's own form on the right. Neither page renders inside
     DefaultLayout (they're top-level routes, not its children — see
     router/index.ts), so DefaultLayout's own locale <select> never reaches
     them; this is the only other place a language switcher can live, and
     both pages require one. Props (title/subtitle) + default slot keep the
     same API, so LoginPage/SetupPage need no changes. -->
<template>
  <div class="auth-shell">
    <!-- Brand panel: dark surface carrying the product identity. This is a
         deliberate two-tone split, not a theme flip — the interactive form
         on the right stays in the app's light theme. -->
    <aside class="auth-brand">
      <div class="auth-brand-inner">
        <div class="auth-brand-mark">
          <img class="auth-brand-logo" :src="logo" alt="" />
          <span class="auth-brand-word">yolorouter</span>
        </div>
        <div class="auth-brand-copy">
          <!-- A slogan, not a section heading — a <p> avoids an h2 landing
               before the form panel's own <h1> title. -->
          <p class="auth-brand-headline">{{ t('auth.brandTagline') }}</p>
          <p class="auth-brand-desc">{{ t('auth.brandDesc') }}</p>
          <ul class="auth-brand-points">
            <li>
              <Network class="auth-brand-point-icon" :size="18" :stroke-width="1.75" />
              <span>{{ t('auth.brandPoint1') }}</span>
            </li>
            <li>
              <BarChart3 class="auth-brand-point-icon" :size="18" :stroke-width="1.75" />
              <span>{{ t('auth.brandPoint2') }}</span>
            </li>
            <li>
              <Server class="auth-brand-point-icon" :size="18" :stroke-width="1.75" />
              <span>{{ t('auth.brandPoint3') }}</span>
            </li>
          </ul>
        </div>
      </div>
    </aside>

    <!-- Form panel: light surface hosting the actual login/setup form. -->
    <main class="auth-form-panel">
      <!-- Wrapper div (not a class on the component) so the absolute
           positioning is robust regardless of what LocaleSwitcher renders
           as its root element. -->
      <div class="auth-locale-select">
        <LocaleSwitcher />
      </div>
      <div class="auth-form-inner">
        <div class="auth-form-head">
          <h1 class="auth-title">{{ title }}</h1>
          <p class="auth-subtitle">{{ subtitle }}</p>
        </div>
        <slot />
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Network, BarChart3, Server } from '@lucide/vue'
import LocaleSwitcher from './LocaleSwitcher.vue'
import logo from '../assets/logo.svg'

defineProps<{ title: string; subtitle: string }>()

const { t } = useI18n()
</script>

<style scoped>
.auth-shell {
  min-height: 100dvh;
  display: grid;
  grid-template-columns: 1.08fr 0.92fr;
  background: var(--color-surface);
}

/* ---- Brand panel ---- */
.auth-brand {
  position: relative;
  overflow: hidden;
  padding: 56px 56px 48px;
  color: oklch(96% 0.01 270);
  background:
    radial-gradient(120% 120% at 12% 6%, oklch(64% 0.17 274 / 0.55) 0%, transparent 46%),
    radial-gradient(120% 120% at 92% 94%, oklch(58% 0.19 292 / 0.42) 0%, transparent 52%),
    linear-gradient(152deg, oklch(32% 0.1 278) 0%, oklch(20% 0.06 274) 100%);
}

/* Technical grid texture, faded from the top-left — reads as a dev-tool
   surface rather than a random gradient blob. Decorative only. */
.auth-brand::before {
  content: '';
  position: absolute;
  inset: 0;
  pointer-events: none;
  background-image:
    linear-gradient(to right, oklch(100% 0 0 / 0.055) 1px, transparent 1px),
    linear-gradient(to bottom, oklch(100% 0 0 / 0.055) 1px, transparent 1px);
  background-size: 46px 46px;
  -webkit-mask-image: radial-gradient(125% 95% at 18% 8%, #000 0%, transparent 72%);
  mask-image: radial-gradient(125% 95% at 18% 8%, #000 0%, transparent 72%);
}

.auth-brand-inner {
  position: relative;
  z-index: 1;
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
  gap: var(--space-12);
  max-width: 460px;
}

.auth-brand-mark {
  display: inline-flex;
  align-items: center;
  gap: var(--space-3);
}

.auth-brand-logo {
  width: 30px;
  height: auto;
  /* The shared mark asset is brand-indigo; force it white for the dark panel. */
  filter: brightness(0) invert(1);
}

.auth-brand-word {
  font-size: var(--text-xl);
  font-weight: 700;
  letter-spacing: -0.01em;
  color: #fff;
}

.auth-brand-headline {
  margin: 0;
  font-size: clamp(1.75rem, 1.2rem + 1.6vw, 2.5rem);
  font-weight: 700;
  line-height: 1.16;
  letter-spacing: -0.02em;
  color: oklch(98% 0.008 270);
}

.auth-brand-desc {
  margin: var(--space-4) 0 0;
  max-width: 36ch;
  font-size: var(--text-sm);
  line-height: 1.65;
  color: oklch(82% 0.03 272);
}

.auth-brand-points {
  list-style: none;
  margin: 28px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.auth-brand-points li {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  font-size: var(--text-sm);
  color: oklch(88% 0.02 272);
}

.auth-brand-point-icon {
  flex: none;
  color: oklch(80% 0.12 282);
}

/* ---- Form panel ---- */
.auth-form-panel {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-10) var(--space-6);
  background: var(--color-surface);
}

.auth-locale-select {
  position: absolute;
  top: var(--space-5);
  right: var(--space-6);
}

.auth-form-inner {
  width: 100%;
  max-width: 384px;
}

.auth-form-head {
  margin-bottom: var(--space-1);
}

.auth-title {
  margin: 0;
  font-size: var(--text-2xl);
  font-weight: 800;
  line-height: 1.15;
  letter-spacing: -0.01em;
  color: var(--color-text);
}

.auth-subtitle {
  margin: var(--space-2) 0 0;
  color: var(--color-text-secondary);
  font-size: var(--text-sm);
  line-height: 1.6;
}

/* Gentle entrance; collapses to static under reduced-motion. */
@media (prefers-reduced-motion: no-preference) {
  .auth-brand-inner,
  .auth-form-inner {
    animation: auth-rise var(--duration-normal, 240ms) var(--ease-out, cubic-bezier(0.16, 1, 0.3, 1)) both;
  }
  .auth-form-inner {
    animation-delay: 60ms;
  }
  @keyframes auth-rise {
    from {
      opacity: 0;
      transform: translateY(10px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }
}

/* ---- Mobile: stack brand strip above the form ---- */
@media (max-width: 860px) {
  .auth-shell {
    grid-template-columns: 1fr;
    grid-template-rows: auto 1fr;
  }
  .auth-brand {
    padding: 32px 28px;
  }
  .auth-brand-inner {
    max-width: none;
    gap: var(--space-5);
  }
  .auth-brand-headline {
    font-size: var(--text-xl);
  }
  /* Keep the mobile brand strip compact — supporting copy lives on desktop. */
  .auth-brand-desc,
  .auth-brand-points {
    display: none;
  }
  .auth-form-panel {
    /* Reserve top room for the absolute locale switcher (top 20px, 34px tall)
       so it never overlaps the heading at narrow widths. */
    padding: 4rem var(--space-5) var(--space-10);
    align-items: flex-start;
  }
}
</style>
