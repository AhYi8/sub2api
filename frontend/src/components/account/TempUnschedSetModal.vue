<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.tempUnschedulable.setTitle')"
    width="normal"
    @close="handleClose"
  >
    <div class="space-y-4">
      <div class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
        {{ t('admin.accounts.tempUnschedulable.setHint') }}
      </div>

      <div class="rounded-lg border border-gray-200 p-4 dark:border-dark-600">
        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.tempUnschedulable.accountName') }}
        </p>
        <p class="mt-1 text-sm font-medium text-gray-900 dark:text-gray-100">
          {{ account?.name || '-' }}
        </p>
      </div>

      <div>
        <label class="input-label" for="temp-unsched-duration">
          {{ t('admin.accounts.tempUnschedulable.durationLabel') }}
        </label>
        <div class="mt-2 flex flex-wrap gap-2">
          <button
            v-for="preset in durationPresets"
            :key="preset"
            type="button"
            class="btn btn-sm"
            :class="durationMinutes === preset ? 'btn-primary' : 'btn-secondary'"
            @click="applyPreset(preset)"
          >
            {{ t('admin.accounts.tempUnschedulable.presetMinutes', { minutes: preset }) }}
          </button>
        </div>
        <input
          id="temp-unsched-duration"
          v-model.number="durationInput"
          type="number"
          min="1"
          max="10080"
          step="1"
          class="input mt-2 w-full"
          :placeholder="t('admin.accounts.tempUnschedulable.durationPlaceholder')"
          data-testid="temp-unsched-duration-input"
        />
        <p class="input-hint mt-1">
          {{ t('admin.accounts.tempUnschedulable.durationRangeHint') }}
        </p>
      </div>

      <div>
        <label class="input-label" for="temp-unsched-reason">
          {{ t('admin.accounts.tempUnschedulable.reasonLabel') }}
        </label>
        <input
          id="temp-unsched-reason"
          v-model="reason"
          type="text"
          maxlength="200"
          class="input mt-1 w-full"
          :placeholder="t('admin.accounts.tempUnschedulable.reasonPlaceholder')"
          data-testid="temp-unsched-reason-input"
        />
        <p class="input-hint mt-1">
          {{ t('admin.accounts.tempUnschedulable.reasonHint') }}
        </p>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!isValid || submitting"
          data-testid="temp-unsched-submit"
          @click="handleSubmit"
        >
          <svg
            v-if="submitting"
            class="-ml-1 mr-2 h-4 w-4 animate-spin"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
            <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"></path>
          </svg>
          {{ t('admin.accounts.tempUnschedulable.submit') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { Account } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{
  show: boolean
  account: Account | null
}>()

const emit = defineEmits<{
  close: []
  set: [account: Account]
}>()

const { t } = useI18n()
const appStore = useAppStore()

// 快捷预设时长（分钟）
const durationPresets = [15, 30, 60, 120]

const durationInput = ref<number | ''>(30)
const reason = ref('')
const submitting = ref(false)

const durationMinutes = computed<number>(() => {
  const value = Number(durationInput.value)
  return Number.isFinite(value) ? value : Number.NaN
})

const isValid = computed(() =>
  Number.isInteger(durationMinutes.value) &&
  durationMinutes.value >= 1 &&
  durationMinutes.value <= 10080
)

const applyPreset = (preset: number) => {
  durationInput.value = preset
}

const resetForm = () => {
  durationInput.value = 30
  reason.value = ''
}

const handleClose = () => {
  emit('close')
}

const handleSubmit = async () => {
  if (!props.account || !isValid.value || submitting.value) return
  submitting.value = true
  try {
    const updated = await adminAPI.accounts.setTempUnschedulable(
      props.account.id,
      durationMinutes.value,
      reason.value.trim()
    )
    appStore.showSuccess(t('admin.accounts.tempUnschedulable.setSuccess'))
    emit('set', updated)
    handleClose()
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.tempUnschedulable.setFailed'))
  } finally {
    submitting.value = false
  }
}

watch(
  () => props.show,
  (visible) => {
    if (visible) resetForm()
  }
)
</script>
