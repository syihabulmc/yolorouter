<!-- frontend/src/components/common/UserFilterSelect.vue
     The account-scope select used in stats page headers. One component so
     the prop set (clearable / filterable / width) and the placeholder stay
     identical everywhere instead of being retyped per page. Pairs with the
     useUserFilter composable. -->
<template>
  <NSelect
    :value="value"
    :options="options"
    :placeholder="t('common.allAccounts')"
    clearable
    filterable
    size="small"
    style="width: 180px"
    @update:value="(v: number | null) => emit('update:value', v)"
  />
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NSelect, type SelectOption } from 'naive-ui'

defineProps<{
  value: number | null
  options: SelectOption[]
}>()
const emit = defineEmits<{ (e: 'update:value', v: number | null): void }>()
const { t } = useI18n()
</script>
