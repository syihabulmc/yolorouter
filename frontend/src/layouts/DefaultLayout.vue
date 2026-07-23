<!-- frontend/src/layouts/DefaultLayout.vue -->
<template>
  <n-layout has-sider class="app-shell">
    <n-layout-sider
      :collapsed="collapsed"
      :collapsed-width="64"
      :width="220"
      collapse-mode="width"
      show-trigger="bar"
      bordered
      :native-scrollbar="false"
      class="layout-sider"
      @update:collapsed="(v: boolean) => (collapsed = v)"
    >
      <div class="sidebar-inner">
        <RouterLink to="/" class="sidebar-logo" :class="{ 'sidebar-logo--collapsed': collapsed }">
          <img :src="logo" alt="" width="26" />
          <span v-if="!collapsed" class="sidebar-logo__title">Yolorouter</span>
        </RouterLink>

        <div class="sidebar-nav-main">
          <SidebarNav
            :items="navItems"
            :collapsed="collapsed"
            :soon-label="t('nav.soonBadge')"
            :soon-tooltip="t('nav.comingSoon')"
          />
        </div>

        <div class="sidebar-bottom">
          <n-dropdown :options="userMenuOptions" placement="top-start" @select="onLogout">
            <button class="sidebar-user" :class="{ 'sidebar-user--collapsed': collapsed }">
              <span class="sidebar-user__avatar">{{ userInitial }}</span>
              <span v-if="!collapsed" class="sidebar-user__name">{{ authStore.username }}</span>
            </button>
          </n-dropdown>
        </div>
      </div>
    </n-layout-sider>

    <n-layout class="layout-main">
      <n-layout-content>
        <div class="layout-content">
          <router-view />
        </div>
      </n-layout-content>
    </n-layout>

    <n-modal
      v-model:show="showChangePassword"
      preset="card"
      :title="t('auth.changePasswordTitle')"
      style="max-width: 400px"
      @after-leave="resetChangePasswordForm"
    >
      <n-form require-mark-placement="left" ref="changePasswordFormRef" :model="changePasswordForm" :rules="changePasswordRules">
        <n-form-item path="currentPassword">
          <template #label>
            <HelpLabel :tip="t('auth.currentPassword_tip')">{{ t('auth.currentPassword') }}</HelpLabel>
          </template>
          <n-input v-model:value="changePasswordForm.currentPassword" type="password" show-password-on="click" />
        </n-form-item>
        <n-form-item path="newPassword">
          <template #label>
            <HelpLabel :tip="t('auth.newPassword_tip')">{{ t('auth.newPassword') }}</HelpLabel>
          </template>
          <n-input v-model:value="changePasswordForm.newPassword" type="password" show-password-on="click" />
        </n-form-item>
        <n-form-item path="confirmNewPassword">
          <template #label>
            <HelpLabel :tip="t('auth.confirmNewPassword_tip')">{{ t('auth.confirmNewPassword') }}</HelpLabel>
          </template>
          <n-input v-model:value="changePasswordForm.confirmNewPassword" type="password" show-password-on="click" />
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="showChangePassword = false">{{ t('common.cancel') }}</n-button>
          <n-button type="primary" :loading="changingPassword" @click="onChangePasswordSubmit">
            {{ t('auth.changePasswordButton') }}
          </n-button>
        </n-space>
      </template>
    </n-modal>

    <n-modal v-model:show="showLanguage" preset="card" :title="t('nav.language')" style="max-width: 340px">
      <div class="lang-options">
        <button
          v-for="lang in LOCALES"
          :key="lang.value"
          type="button"
          class="lang-option"
          :class="{ 'lang-option--active': lang.value === localeStore.locale }"
          @click="onSelectLanguage(lang.value)"
        >
          <span>{{ lang.label }}</span>
          <NIcon v-if="lang.value === localeStore.locale" :size="16"><Check /></NIcon>
        </button>
      </div>
    </n-modal>
  </n-layout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { NIcon, useDialog, useMessage, type DropdownOption, type FormInst, type FormRules } from 'naive-ui'
import SidebarNav, { type NavItem } from '../components/SidebarNav.vue'
import {
  BarChart3,
  Box,
  Check,
  Info,
  Key,
  KeyRound,
  Languages,
  LayoutGrid,
  Receipt,
  ScrollText,
  Server,
  Tags,
  TrendingDown,
} from '@lucide/vue'
import { useAuthStore } from '../store/auth'
import { useUpdateStore } from '../store/update'
import { useLocaleStore } from '../store/locale'
import { LOCALES, type Locale } from '../i18n'
import { APIError, displayMessage } from '../api/client'
import { ACCOUNT_SESSION_INVALID } from '../api/errcodes'
import { passwordStrengthRule, confirmPasswordRule } from '../utils/authValidators'
import HelpLabel from '../components/HelpLabel.vue'
import logo from '../assets/logo.svg'

const { t } = useI18n()
const router = useRouter()
const authStore = useAuthStore()
const dialog = useDialog()
const message = useMessage()
const localeStore = useLocaleStore()

const collapsed = ref(false)
const updateStore = useUpdateStore()

// Fire the background update check once when the admin shell mounts, so the
// sidebar badge reflects "new version available" without the user having to
// visit the system page first. checkForUpdates swallows its own errors (a
// failed check is an expected pre-public / GitHub-outage state), so
// fire-and-forget here is safe — no unhandled rejection.
onMounted(() => {
  void updateStore.checkForUpdates()
})

// A single ordered list where static group headers interleave with standalone
// links, so the sidebar renders the full information architecture in one pass.
// computed rather than a plain array so labels (and the update badge) stay in
// sync when the user switches locale or a new version is detected.
//
// Short nav.* labels are used here (e.g. "用量统计") rather than the pages'
// own longer PageHeader titles, so the sidebar reads cleanly while each page
// keeps its fuller heading. Entries carrying `disabled` are placeholders for
// features that aren't built yet. `onClick` entries open a modal instead of
// navigating.
const navItems = computed<NavItem[]>(() => [
  { key: 'overview', label: t('nav.overview'), icon: LayoutGrid, to: '/' },

  { key: 'group-analytics', label: t('nav.groupAnalytics'), group: true },
  { key: 'usage', label: t('nav.usage'), icon: BarChart3, to: '/analytics' },
  { key: 'log-audit', label: t('nav.logAudit'), icon: ScrollText, to: '/request-logs' },
  { key: 'cost-stats', label: t('nav.costStats'), icon: Receipt, disabled: true },

  { key: 'group-models', label: t('nav.groupModels'), group: true },
  { key: 'providers', label: t('nav.providers'), icon: Server, to: '/providers' },
  { key: 'models', label: t('nav.models'), icon: Box, to: '/models' },

  { key: 'tokens', label: t('nav.tokens'), icon: Key, to: '/api-keys' },

  { key: 'cost-optimization', label: t('nav.costOptimization'), icon: TrendingDown, disabled: true },

  { key: 'group-system', label: t('nav.groupSystem'), group: true },
  { key: 'system-info', label: t('nav.systemInfo'), icon: Info, to: '/system', badge: updateStore.hasUpdate },
  { key: 'language', label: t('nav.language'), icon: Languages, onClick: () => (showLanguage.value = true) },
  { key: 'change-password', label: t('auth.changePasswordTitle'), icon: KeyRound, onClick: () => (showChangePassword.value = true) },
  { key: 'model-pricing', label: t('nav.modelPricing'), icon: Tags, disabled: true },
])

// Just "logout" now — change password moved into the System Settings group in
// the sidebar. computed rather than a plain array so the label stays in sync
// when the user switches locale without needing to re-open the dropdown.
const userMenuOptions = computed<DropdownOption[]>(() => [
  { label: t('auth.logout'), key: 'logout' },
])

// A single-character avatar fallback (no avatar upload in this admin
// tool) — uppercased so a lowercase username still reads as a deliberate
// initial, not a truncation artifact.
const userInitial = computed(() => (authStore.username?.[0] ?? '?').toUpperCase())

// Logout is the dropdown's only entry, so this runs straight from @select
// without switching on the key.
function onLogout() {
  dialog.warning({
    title: t('auth.logoutConfirmTitle'),
    content: t('auth.logoutConfirmContent'),
    positiveText: t('auth.logout'),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      try {
        await authStore.logout()
      } catch (err) {
        if (err instanceof APIError && err.code === ACCOUNT_SESSION_INVALID) {
          // The session was already gone server-side — api/auth.ts's
          // session-invalid handling already cleared local auth state
          // and navigated to /login, so there's nothing left to do here.
          return
        }
        message.error(displayMessage(err, t))
        return
      }
      router.push('/login')
    },
  })
}

// Language picker — the corner switcher was removed in favor of a "Language"
// entry in the System Settings group, which opens this modal. The option list
// (LOCALES) and check-mark treatment are shared with the LocaleSwitcher.
const showLanguage = ref(false)
function onSelectLanguage(value: Locale) {
  localeStore.setLocale(value)
  showLanguage.value = false
}

const showChangePassword = ref(false)
const changingPassword = ref(false)
const changePasswordFormRef = ref<FormInst | null>(null)
const changePasswordForm = reactive({ currentPassword: '', newPassword: '', confirmNewPassword: '' })

// Cleared whenever the modal fully closes (@after-leave, below — fires for
// every close path: cancel, backdrop click, Esc, and the success path's
// own showChangePassword = false), not just on successful submit. Plain
// reactive state that outlives the modal being open would otherwise let
// anyone who regains access to an already-logged-in tab reopen the modal
// and read back a previously-typed password via "show password".
function resetChangePasswordForm() {
  changePasswordForm.currentPassword = ''
  changePasswordForm.newPassword = ''
  changePasswordForm.confirmNewPassword = ''
}

// computed rather than a plain object so the messages stay in the current
// locale if the user switches language while the modal happens to be open
// — matches userMenuOptions above.
const changePasswordRules = computed<FormRules>(() => ({
  currentPassword: [{ required: true, message: t('auth.fieldRequired'), trigger: ['blur', 'input'] }],
  newPassword: [passwordStrengthRule(t)],
  confirmNewPassword: [confirmPasswordRule(t, () => changePasswordForm.newPassword)],
}))

async function onChangePasswordSubmit() {
  try {
    await changePasswordFormRef.value?.validate()
  } catch {
    return
  }

  dialog.warning({
    title: t('auth.changePasswordConfirmTitle'),
    content: t('auth.changePasswordConfirmContent'),
    positiveText: t('auth.changePasswordButton'),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      changingPassword.value = true
      try {
        await authStore.changePassword(changePasswordForm.currentPassword, changePasswordForm.newPassword)
        showChangePassword.value = false
        message.success(t('auth.changePasswordSuccess'))
        router.push('/login')
      } catch (err) {
        if (err instanceof APIError && err.code === ACCOUNT_SESSION_INVALID) {
          // api/auth.ts's session-invalid handling already cleared local
          // auth state and navigated to /login — just close this modal
          // and explain why, instead of leaving it open on top of the
          // login page.
          showChangePassword.value = false
          message.error(err.message)
        } else {
          message.error(displayMessage(err, t))
        }
      } finally {
        changingPassword.value = false
      }
    },
  })
}
</script>

<style scoped>
.layout-sider {
  background-color: var(--color-sidebar);
}

.sidebar-inner {
  display: flex;
  flex-direction: column;
  height: 100dvh;
  overflow: hidden;
}

.sidebar-logo {
  display: flex;
  flex-shrink: 0;
  align-items: center;
  gap: 0.5rem;
  height: 64px;
  padding: 0 16px;
  color: var(--color-text);
  font-weight: 700;
}

.sidebar-logo--collapsed {
  justify-content: center;
  padding: 0;
}

/* The nav is the one flexible region: it takes the space between the fixed logo
   and account footer and scrolls internally, so on short viewports every entry
   (including language / password / logout) stays reachable instead of being
   clipped. min-height:0 lets a flex child actually shrink below its content
   height, which is what enables the scroll. */
.sidebar-nav-main {
  flex: 1 1 auto;
  min-height: 0;
  margin-top: var(--space-2);
  overflow-y: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
  scrollbar-color: var(--color-border) transparent;
}

.sidebar-nav-main::-webkit-scrollbar {
  width: 6px;
}

.sidebar-nav-main::-webkit-scrollbar-thumb {
  background: var(--color-border);
  border-radius: var(--radius-full);
}

.sidebar-bottom {
  display: flex;
  flex-shrink: 0;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3) 16px var(--space-4);
  border-top: 1px solid var(--color-border);
}

.sidebar-user {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 44px;
  padding: 0 8px;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text);
  font: inherit;
  cursor: pointer;
  transition: background var(--duration-fast) var(--ease-out);
}

.sidebar-user:hover {
  background: var(--color-surface-hover);
}

.sidebar-user--collapsed {
  justify-content: center;
  padding: 0;
}

.sidebar-user__avatar {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  border-radius: var(--radius-full);
  background: var(--color-accent);
  color: #fff;
  font-size: var(--text-xs);
  font-weight: 700;
}

.sidebar-user__name {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
  font-size: var(--text-sm);
  font-weight: 600;
}

.layout-main {
  background: transparent;
}

.layout-content {
  position: relative;
  height: 100dvh;
  overflow: auto;
}

/* Language picker rows inside the modal — a check mark on the right marks the
   active language, matching the shared LocaleSwitcher's treatment. */
.lang-options {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}

.lang-option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 42px;
  padding: 0 12px;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text);
  font: inherit;
  font-size: var(--text-sm);
  text-align: left;
  cursor: pointer;
  transition: background var(--duration-fast) var(--ease-out);
}

.lang-option:hover {
  background: var(--color-surface-hover);
}

.lang-option--active {
  color: var(--color-accent);
  font-weight: 600;
}

@media (max-width: 640px) {
  .sidebar-bottom {
    padding: var(--space-3) 6px var(--space-4);
  }
}
</style>
