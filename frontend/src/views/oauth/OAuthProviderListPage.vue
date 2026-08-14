<!-- frontend/src/views/oauth/OAuthProviderListPage.vue
     External-login provider management. One generic form covers every
     standard OAuth2/OIDC provider; presets prefill the popular ones and
     the OIDC discovery button fills the endpoints from a well-known URL.
     The client secret is write-only: the form never shows the stored
     value, and leaving the field blank on edit keeps it. -->
<template>
  <div class="common-page">
    <PageHeader
      :eyebrow="t('oauthProviders.eyebrow')"
      :title="t('oauthProviders.pageTitle')"
      :description="t('oauthProviders.pageDescription')"
    >
      <template #actions>
        <NButton type="primary" @click="openCreate">{{ t('oauthProviders.createButton') }}</NButton>
      </template>
    </PageHeader>

    <EmptyState v-if="!loading && providers.length === 0" :icon="LogIn" :title="t('oauthProviders.listEmpty')">
      <template #action>
        <NButton type="primary" @click="openCreate">{{ t('oauthProviders.createButton') }}</NButton>
      </template>
    </EmptyState>
    <NDataTable
      v-else
      :columns="columns"
      :data="providers"
      :loading="loading"
      :row-key="(row: OAuthProviderView) => row.id"
      :bordered="false"
    />

    <NModal
      v-model:show="showModal"
      preset="card"
      :title="editing ? t('oauthProviders.editTitle') : t('oauthProviders.createTitle')"
      style="max-width: 520px"
      :mask-closable="false"
      :close-on-esc="false"
    >
      <NForm ref="formRef" :model="form" :rules="rules" label-placement="top" require-mark-placement="left">
        <NFormItem v-if="!editing">
          <template #label>
            <HelpLabel :tip="t('oauthProviders.presetLabel_tip')">{{ t('oauthProviders.presetLabel') }}</HelpLabel>
          </template>
          <NSelect
            :value="preset"
            :options="presetOptions"
            :placeholder="t('oauthProviders.presetPlaceholder')"
            clearable
            @update:value="applyPreset"
          />
        </NFormItem>

        <div class="form-grid">
          <NFormItem path="name">
            <template #label>
              <HelpLabel :tip="t('oauthProviders.nameLabel_tip')">{{ t('oauthProviders.nameLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.name" :placeholder="t('oauthProviders.namePlaceholder')" />
          </NFormItem>
          <NFormItem path="slug">
            <template #label>
              <HelpLabel :tip="t('oauthProviders.slugLabel_tip')">{{ t('oauthProviders.slugLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.slug" :disabled="editing !== null" :placeholder="t('oauthProviders.slugPlaceholder')" />
          </NFormItem>
        </div>

        <NFormItem>
          <template #label>
            <HelpLabel :tip="t('oauthProviders.wellKnownLabel_tip')">{{ t('oauthProviders.wellKnownLabel') }}</HelpLabel>
          </template>
          <NInput v-model:value="wellKnownURL" :placeholder="t('oauthProviders.wellKnownPlaceholder')" />
          <NButton class="discover-btn" :loading="discovering" @click="runDiscovery">
            {{ t('oauthProviders.discoverButton') }}
          </NButton>
        </NFormItem>

        <NFormItem path="authorization_endpoint">
          <template #label>
            <HelpLabel :tip="t('oauthProviders.authorizeLabel_tip')">{{ t('oauthProviders.authorizeLabel') }}</HelpLabel>
          </template>
          <NInput v-model:value="form.authorization_endpoint" placeholder="https://idp.example.com/authorize" />
        </NFormItem>
        <NFormItem path="token_endpoint">
          <template #label>
            <HelpLabel :tip="t('oauthProviders.tokenLabel_tip')">{{ t('oauthProviders.tokenLabel') }}</HelpLabel>
          </template>
          <NInput v-model:value="form.token_endpoint" placeholder="https://idp.example.com/token" />
        </NFormItem>
        <NFormItem path="userinfo_endpoint">
          <template #label>
            <HelpLabel :tip="t('oauthProviders.userinfoLabel_tip')">{{ t('oauthProviders.userinfoLabel') }}</HelpLabel>
          </template>
          <NInput v-model:value="form.userinfo_endpoint" placeholder="https://idp.example.com/userinfo" />
        </NFormItem>

        <div class="form-grid">
          <NFormItem path="client_id">
            <template #label>
              <HelpLabel :tip="t('oauthProviders.clientIdLabel_tip')">{{ t('oauthProviders.clientIdLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.client_id" />
          </NFormItem>
          <NFormItem :path="editing ? undefined : 'client_secret'">
            <template #label>
              <HelpLabel :tip="t('oauthProviders.clientSecretLabel_tip')">{{ t('oauthProviders.clientSecretLabel') }}</HelpLabel>
            </template>
            <NInput
              v-model:value="form.client_secret"
              type="password"
              show-password-on="click"
              :placeholder="editing ? t('oauthProviders.secretKeepPlaceholder') : ''"
            />
          </NFormItem>
        </div>

        <div class="form-grid">
          <NFormItem>
            <template #label>
              <HelpLabel :tip="t('oauthProviders.scopesLabel_tip')">{{ t('oauthProviders.scopesLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.scopes" placeholder="openid profile email" />
          </NFormItem>
          <NFormItem>
            <template #label>
              <HelpLabel :tip="t('oauthProviders.authStyleLabel_tip')">{{ t('oauthProviders.authStyleLabel') }}</HelpLabel>
            </template>
            <NSelect v-model:value="form.auth_style" :options="authStyleOptions" />
          </NFormItem>
        </div>

        <NDivider class="mapping-divider">{{ t('oauthProviders.mappingDivider') }}</NDivider>
        <div class="form-grid">
          <NFormItem>
            <template #label>
              <HelpLabel :tip="t('oauthProviders.userIdFieldLabel_tip')">{{ t('oauthProviders.userIdFieldLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.user_id_field" placeholder="sub" />
          </NFormItem>
          <NFormItem>
            <template #label>
              <HelpLabel :tip="t('oauthProviders.usernameFieldLabel_tip')">{{ t('oauthProviders.usernameFieldLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.username_field" placeholder="preferred_username" />
          </NFormItem>
          <NFormItem>
            <template #label>
              <HelpLabel :tip="t('oauthProviders.displayNameFieldLabel_tip')">{{ t('oauthProviders.displayNameFieldLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.display_name_field" placeholder="name" />
          </NFormItem>
          <NFormItem>
            <template #label>
              <HelpLabel :tip="t('oauthProviders.emailFieldLabel_tip')">{{ t('oauthProviders.emailFieldLabel') }}</HelpLabel>
            </template>
            <NInput v-model:value="form.email_field" placeholder="email" />
          </NFormItem>
        </div>

        <NFormItem>
          <template #label>
            <HelpLabel :tip="t('oauthProviders.enabledLabel_tip')">{{ t('oauthProviders.enabledLabel') }}</HelpLabel>
          </template>
          <NSwitch v-model:value="form.enabled" />
        </NFormItem>
      </NForm>
      <template #footer>
        <NSpace justify="end">
          <NButton @click="showModal = false">{{ t('common.cancel') }}</NButton>
          <NButton type="primary" :loading="saving" @click="save">{{ t('common.save') }}</NButton>
        </NSpace>
      </template>
    </NModal>
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NButton, NDataTable, NDivider, NForm, NFormItem, NInput, NModal,
  NSelect, NSpace, NSwitch, NTag,
  useDialog, useMessage,
  type DataTableColumns, type FormInst, type FormRules, type SelectOption,
} from 'naive-ui'
import { LogIn } from '@lucide/vue'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import HelpLabel from '../../components/HelpLabel.vue'
import { columnTitle, STATUS_COL_WIDTH } from '../../utils/columnTitle'
import { displayMessage } from '../../api/client'
import {
  createOAuthProvider, deleteOAuthProvider, discoverOIDC, listOAuthProviders,
  updateOAuthProvider, type OAuthProviderInput, type OAuthProviderView,
} from '../../api/oauth'

const { t } = useI18n()
const message = useMessage()
const dialog = useDialog()

// === List =================================================================

const providers = ref<OAuthProviderView[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await listOAuthProviders()
    providers.value = res.providers
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
})

async function toggleEnabled(row: OAuthProviderView, value: boolean) {
  try {
    await updateOAuthProvider(row.id, { enabled: value })
    row.enabled = value
  } catch (err) {
    message.error(displayMessage(err, t))
  }
}

function confirmDelete(row: OAuthProviderView) {
  dialog.warning({
    title: t('oauthProviders.confirmDeleteTitle'),
    content:
      row.identity_count > 0
        ? t('oauthProviders.confirmDeleteWithIdentities', { name: row.name, count: row.identity_count })
        : t('oauthProviders.confirmDeleteContent', { name: row.name }),
    positiveText: t('common.confirm'),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      try {
        await deleteOAuthProvider(row.id)
        message.success(t('oauthProviders.deleted'))
        void load()
      } catch (err) {
        message.error(displayMessage(err, t))
      }
    },
  })
}

const columns = computed<DataTableColumns<OAuthProviderView>>(() => [
  {
    title: columnTitle(t('oauthProviders.nameColumn'), t('oauthProviders.nameColumn_tip')),
    key: 'name',
    minWidth: 160,
    render: (row) =>
      h('div', {}, [
        h('div', {}, row.name),
        h('div', { class: 'slug-sub' }, row.slug),
      ]),
  },
  {
    title: columnTitle(t('oauthProviders.enabledColumn'), t('oauthProviders.enabledColumn_tip')),
    key: 'enabled',
    width: STATUS_COL_WIDTH,
    render: (row) =>
      h(NSwitch, {
        value: row.enabled,
        size: 'small',
        'onUpdate:value': (v: boolean) => void toggleEnabled(row, v),
      }),
  },
  {
    title: columnTitle(t('oauthProviders.identitiesColumn'), t('oauthProviders.identitiesColumn_tip')),
    key: 'identity_count',
    width: 110,
    render: (row) =>
      h(NTag, { size: 'small', bordered: false, type: row.identity_count > 0 ? 'info' : 'default' },
        { default: () => String(row.identity_count) }),
  },
  {
    title: columnTitle(t('oauthProviders.updatedColumn'), t('oauthProviders.updatedColumn_tip')),
    key: 'updated_at',
    width: 180,
    render: (row) => new Date(row.updated_at).toLocaleString(),
  },
  {
    title: t('common.actions'),
    key: 'actions',
    width: 150,
    render: (row) =>
      h(NSpace, { size: 'small' }, {
        default: () => [
          h(NButton, { size: 'small', onClick: () => openEdit(row) }, { default: () => t('common.edit') }),
          h(NButton, { size: 'small', type: 'error', quaternary: true, onClick: () => confirmDelete(row) },
            { default: () => t('common.delete') }),
        ],
      }),
  },
])

// === Create / edit modal ==================================================

const showModal = ref(false)
const editing = ref<OAuthProviderView | null>(null)
const saving = ref(false)
const formRef = ref<FormInst | null>(null)
const wellKnownURL = ref('')
const discovering = ref(false)
const preset = ref<string | null>(null)

function emptyForm(): OAuthProviderInput {
  return {
    slug: '', name: '', icon: '', enabled: true,
    client_id: '', client_secret: '',
    authorization_endpoint: '', token_endpoint: '', userinfo_endpoint: '',
    scopes: 'openid profile email',
    user_id_field: 'sub', username_field: 'preferred_username',
    display_name_field: 'name', email_field: 'email',
    auth_style: 'post',
  }
}

const form = ref<OAuthProviderInput>(emptyForm())

const rules = computed<FormRules>(() => {
  const required = { required: true, message: t('oauthProviders.fieldRequired'), trigger: ['blur', 'input'] }
  return {
    name: [required],
    slug: [required],
    client_id: [required],
    client_secret: [required],
    authorization_endpoint: [required],
    token_endpoint: [required],
    userinfo_endpoint: [required],
  }
})

const authStyleOptions = computed<SelectOption[]>(() => [
  { label: t('oauthProviders.authStylePost'), value: 'post' },
  { label: t('oauthProviders.authStyleBasic'), value: 'basic' },
])

// Presets prefill the form for the popular providers; everything stays
// editable afterwards. The generic OIDC preset just resets to defaults —
// its endpoints come from discovery.
const presetOptions = computed<SelectOption[]>(() => [
  { label: t('oauthProviders.presetOIDC'), value: 'oidc' },
  { label: 'GitHub', value: 'github' },
  { label: 'Google', value: 'google' },
])

function applyPreset(v: string | null) {
  preset.value = v
  if (!v) return
  const base = emptyForm()
  base.enabled = form.value.enabled
  base.client_id = form.value.client_id
  base.client_secret = form.value.client_secret
  switch (v) {
    case 'github':
      Object.assign(base, {
        slug: 'github', name: 'GitHub',
        authorization_endpoint: 'https://github.com/login/oauth/authorize',
        token_endpoint: 'https://github.com/login/oauth/access_token',
        userinfo_endpoint: 'https://api.github.com/user',
        scopes: 'read:user user:email',
        user_id_field: 'id', username_field: 'login',
        display_name_field: 'name', email_field: 'email',
      })
      break
    case 'google':
      Object.assign(base, {
        slug: 'google', name: 'Google',
        authorization_endpoint: 'https://accounts.google.com/o/oauth2/v2/auth',
        token_endpoint: 'https://oauth2.googleapis.com/token',
        userinfo_endpoint: 'https://openidconnect.googleapis.com/v1/userinfo',
        username_field: 'email',
      })
      break
    case 'oidc':
      base.slug = form.value.slug
      base.name = form.value.name
      break
  }
  form.value = base
}

async function runDiscovery() {
  if (!wellKnownURL.value.trim()) return
  discovering.value = true
  try {
    const doc = await discoverOIDC(wellKnownURL.value.trim())
    form.value.authorization_endpoint = doc.authorization_endpoint
    form.value.token_endpoint = doc.token_endpoint
    // Unconditional: a document without a userinfo endpoint must clear a
    // stale one from a previous provider, not silently keep it — the
    // required-field validation then forces an explicit value.
    form.value.userinfo_endpoint = doc.userinfo_endpoint
    message.success(t('oauthProviders.discoverSuccess'))
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    discovering.value = false
  }
}

function openCreate() {
  editing.value = null
  preset.value = null
  wellKnownURL.value = ''
  form.value = emptyForm()
  showModal.value = true
}

function openEdit(row: OAuthProviderView) {
  editing.value = row
  wellKnownURL.value = ''
  form.value = {
    slug: row.slug, name: row.name, icon: row.icon, enabled: row.enabled,
    client_id: row.client_id, client_secret: '',
    authorization_endpoint: row.authorization_endpoint,
    token_endpoint: row.token_endpoint,
    userinfo_endpoint: row.userinfo_endpoint,
    scopes: row.scopes,
    user_id_field: row.user_id_field, username_field: row.username_field,
    display_name_field: row.display_name_field, email_field: row.email_field,
    auth_style: row.auth_style,
  }
  showModal.value = true
}

async function save() {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      // Sparse PATCH: the secret rides along only when the admin typed a
      // replacement — an empty field means "keep the stored one".
      const patch: Partial<OAuthProviderInput> = { ...form.value }
      delete patch.slug
      if (!form.value.client_secret) delete patch.client_secret
      await updateOAuthProvider(editing.value.id, patch)
    } else {
      await createOAuthProvider(form.value)
    }
    message.success(t('oauthProviders.saved'))
    showModal.value = false
    void load()
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.form-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0 16px;
}

.discover-btn {
  margin-left: 8px;
  flex-shrink: 0;
}

.mapping-divider {
  margin: 4px 0 12px;
  font-size: 12px;
}

:deep(.slug-sub) {
  font-size: 12px;
  color: #999;
}
</style>
