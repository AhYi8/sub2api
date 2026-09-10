<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.scheduledTests.global.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.scheduledTests.global.description') }}
      </p>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-8">
      <Icon name="refresh" size="md" class="animate-spin text-gray-400" :stroke-width="2" />
      <span class="ml-2 text-sm text-gray-500">{{ t('common.loading') }}...</span>
    </div>

    <div v-else class="space-y-5 p-6">
      <!-- 启用开关 -->
      <div class="flex items-center justify-between">
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.scheduledTests.global.enabled') }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.scheduledTests.global.enabledHint') }}
          </p>
        </div>
        <Toggle v-model="form.enabled" />
      </div>

      <!-- 执行频率 -->
      <div>
        <label class="input-label">{{ t('admin.scheduledTests.global.frequency') }}</label>
        <div class="mt-1.5 flex flex-wrap items-center gap-2">
          <select v-model="frequencyMode" class="input max-w-48">
            <option value="hourly">{{ t('admin.scheduledTests.global.frequencyHourly') }}</option>
            <option value="daily">{{ t('admin.scheduledTests.global.frequencyDaily') }}</option>
            <option value="custom">{{ t('admin.scheduledTests.global.frequencyCustom') }}</option>
          </select>
          <input
            v-if="frequencyMode === 'daily'"
            v-model="dailyTime"
            type="time"
            class="input max-w-32"
          />
          <input
            v-if="frequencyMode === 'custom'"
            v-model="form.cron_expression"
            :placeholder="'0 * * * *'"
            class="input max-w-48 font-mono"
          />
        </div>
        <p class="mt-1.5 text-xs text-gray-400">
          {{ t('admin.scheduledTests.global.frequencyHint') }}
        </p>
        <p v-if="frequencyMode === 'custom'" class="mt-1 font-mono text-xs text-gray-400 dark:text-gray-500">
          cron: {{ form.cron_expression }}
        </p>
      </div>

      <!-- 自动恢复 -->
      <div class="flex items-center justify-between">
        <div>
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.scheduledTests.autoRecover') }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.scheduledTests.autoRecoverHelp') }}
          </p>
        </div>
        <Toggle v-model="form.auto_recover" />
      </div>

      <!-- 每平台测试模型 -->
      <div>
        <label class="input-label">{{ t('admin.scheduledTests.global.platformModels') }}</label>
        <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.scheduledTests.global.platformModelsHint') }}
        </p>
        <div class="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2">
          <div v-for="platform in CONCRETE_PLATFORM_OPTIONS" :key="platform.value">
            <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
              {{ platform.label }}
            </label>
            <input
              v-model="form.platform_models[platform.value]"
              type="text"
              :placeholder="t('admin.scheduledTests.global.modelPlaceholder')"
              class="input"
            />
          </div>
        </div>
      </div>

      <!-- 最大结果数 -->
      <div class="max-w-md">
        <label class="input-label">{{ t('admin.scheduledTests.maxResults') }}</label>
        <input
          v-model.number="form.max_results"
          type="number"
          min="1"
          class="input"
        />
        <p class="mt-1 text-xs text-gray-400">
          {{ t('admin.scheduledTests.global.maxResultsHint') }}
        </p>
      </div>

      <!-- 最近/下次运行 -->
      <div
        v-if="plan && (plan.last_run_at || plan.next_run_at)"
        class="flex flex-wrap gap-6 text-xs text-gray-500 dark:text-gray-400"
      >
        <div v-if="plan.last_run_at">
          <div>{{ t('admin.scheduledTests.lastRun') }}</div>
          <div>{{ formatDateTime(plan.last_run_at) }}</div>
        </div>
        <div v-if="plan.next_run_at && plan.enabled">
          <div>{{ t('admin.scheduledTests.nextRun') }}</div>
          <div>{{ formatDateTime(plan.next_run_at) }}</div>
        </div>
      </div>

      <!-- 保存 -->
      <div class="flex items-center justify-end gap-3">
        <!-- 保存失败：保留草稿但明确标记未保存，提供恢复服务端状态的入口 -->
        <div v-if="saveFailed" class="flex items-center gap-2 text-xs text-amber-600 dark:text-amber-400">
          <span>{{ t('admin.scheduledTests.global.unsavedHint') }}</span>
          <button
            class="text-primary-600 hover:underline dark:text-primary-400"
            @click="loadGlobal"
          >
            {{ t('admin.scheduledTests.global.reload') }}
          </button>
        </div>
        <button
          class="btn btn-primary text-sm"
          :disabled="saving || !canSave"
          @click="handleSave"
        >
          <Icon v-if="saving" name="refresh" size="sm" class="animate-spin" :stroke-width="2" />
          {{ t('common.save') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import { Icon } from '@/components/icons'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { ScheduledTestPlan } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const saving = ref(false)
const plan = ref<ScheduledTestPlan | null>(null)
// 最近一次保存是否失败：失败时保留草稿但显示“未保存”标记
const saveFailed = ref(false)

// 频率快捷方式：hourly/daily 最终仍落成 5 字段 cron 表达式
const frequencyMode = ref<'hourly' | 'daily' | 'custom'>('hourly')
const dailyTime = ref('00:00')

const form = reactive({
  enabled: false,
  cron_expression: '0 * * * *',
  auto_recover: true,
  max_results: 50,
  platform_models: {} as Record<string, string>
})

// 初始化平台模型输入框（未配置的平台显示为空 = 跳过该平台）
const initPlatformModels = (models: Record<string, string> | undefined) => {
  for (const platform of CONCRETE_PLATFORM_OPTIONS) {
    form.platform_models[platform.value] = models?.[platform.value] ?? ''
  }
}

// 从 cron 表达式推断频率模式，仅识别本组件生成的两种快捷形式；
// 返回值同时携带解析出的 daily 时间，避免探测函数带副作用。
const detectFrequencyMode = (cron: string): { mode: 'hourly' | 'daily' | 'custom'; dailyTime?: string } => {
  if (cron === '0 * * * *') return { mode: 'hourly' }
  const dailyMatch = cron.match(/^(\d{1,2}) (\d{1,2}) \* \* \*$/)
  if (dailyMatch) {
    const hh = dailyMatch[2].padStart(2, '0')
    const mm = dailyMatch[1].padStart(2, '0')
    return { mode: 'daily', dailyTime: `${hh}:${mm}` }
  }
  return { mode: 'custom' }
}

// 校验“每天固定时间”输入是否为合法 HH:mm：
// 清空输入会得到 ''，异常来源可能出现 99:99/24:00/12:60，
// 需同时校验格式与小时(00-23)/分钟(00-59)范围，避免生成后端拒绝的 cron。
const isDailyTimeValid = (): boolean => {
  return /^([01]\d|2[0-3]):[0-5]\d$/.test(dailyTime.value)
}

const buildCronExpression = (): string => {
  if (frequencyMode.value === 'hourly') return '0 * * * *'
  if (frequencyMode.value === 'daily') {
    const [hh, mm] = dailyTime.value.split(':')
    return `${Number(mm)} ${Number(hh)} * * *`
  }
  return form.cron_expression.trim()
}

// 将服务端返回的计划同步回表单：保存成功后服务端会做规范化
// （trim、丢弃空模型、重算 next_run_at），必须回填避免本地与服务端漂移。
const applyPlanToForm = (p: ScheduledTestPlan) => {
  form.enabled = p.enabled
  form.cron_expression = p.cron_expression || '0 * * * *'
  form.auto_recover = p.auto_recover
  form.max_results = p.max_results || 50
  initPlatformModels(p.platform_models)
  const detected = detectFrequencyMode(form.cron_expression)
  frequencyMode.value = detected.mode
  if (detected.dailyTime) dailyTime.value = detected.dailyTime
}

const loadGlobal = async () => {
  loading.value = true
  try {
    plan.value = await adminAPI.scheduledTests.getGlobal()
    applyPlanToForm(plan.value)
    saveFailed.value = false
  } catch (error: any) {
    appStore.showError(error?.message || 'Failed to load global scheduled test')
  } finally {
    loading.value = false
  }
}

const canSave = computed(() => {
  // “每天固定时间”模式下时间输入必须合法，避免生成 "NaN NaN * * *" 等无效 cron
  if (frequencyMode.value === 'daily') return isDailyTimeValid()
  return true
})

const handleSave = async () => {
  if (!canSave.value) return
  saving.value = true
  try {
    plan.value = await adminAPI.scheduledTests.updateGlobal({
      cron_expression: buildCronExpression(),
      enabled: form.enabled,
      auto_recover: form.auto_recover,
      max_results: Number(form.max_results) || 50,
      platform_models: { ...form.platform_models }
    })
    // 用服务端规范化后的配置重建表单，保证展示与实际生效一致
    applyPlanToForm(plan.value)
    saveFailed.value = false
    appStore.showSuccess(t('admin.scheduledTests.updateSuccess'))
  } catch (error: any) {
    // 保留用户草稿便于修正后重试，但显著标记“未保存”，
    // 避免把本地编辑值误认为已生效配置
    saveFailed.value = true
    appStore.showError(error?.message || 'Failed to save global scheduled test')
  } finally {
    saving.value = false
  }
}

onMounted(loadGlobal)
</script>
