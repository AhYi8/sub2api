import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const {
  createAccountMock,
  probeUpstreamBillingMock,
  syncUpstreamModelsMock,
  showWarningMock,
  showErrorMock,
  showSuccessMock,
  importCodexSessionMock,
  createOpenAICodexPATMock,
  checkAPIKeysDuplicateMock,
  authIsSimpleMode,
} = vi.hoisted(() => ({
  createAccountMock: vi.fn(),
  probeUpstreamBillingMock: vi.fn(),
  syncUpstreamModelsMock: vi.fn(),
  showWarningMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn(),
  importCodexSessionMock: vi.fn(),
  createOpenAICodexPATMock: vi.fn(),
  checkAPIKeysDuplicateMock: vi.fn(),
  authIsSimpleMode: { value: true },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock,
    showWarning: showWarningMock,
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get isSimpleMode() {
      return authIsSimpleMode.value
    },
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      create: createAccountMock,
      probeUpstreamBilling: probeUpstreamBillingMock,
      syncUpstreamModels: syncUpstreamModelsMock,
      checkAPIKeysDuplicate: checkAPIKeysDuplicateMock,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
      importCodexSession: importCodexSessionMock,
      createOpenAICodexPAT: createOpenAICodexPATMock,
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({}),
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([]),
    },
  },
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn().mockResolvedValue([]),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    // 带插值参数时返回 key + JSON 参数，便于对序号、错误信息等内容做强断言
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key,
    }),
  }
})

import CreateAccountModal from '../CreateAccountModal.vue'

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  props: {
    showManualOption: Boolean,
    showCodexSessionImportOption: Boolean,
    showAgentIdentityOption: Boolean,
    showCodexPatOption: Boolean,
    initialInputMethod: String,
  },
  data: () => ({ inputMethod: 'manual' }),
  emits: ['import-codex-session', 'import-codex-pat'],
  template: `
    <div>
      <button data-testid="import-codex-session" @click="$emit('import-codex-session', 'session-json')">session</button>
      <button data-testid="import-codex-pat" @click="$emit('import-codex-pat', 'pat-token')">pat</button>
    </div>
  `,
})

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  template: `
    <button
      type="button"
      data-testid="select-pricing-groups"
      @click="$emit('update:modelValue', [1, 2])"
    >
      groups
    </button>
  `,
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => [],
    },
    platform: String,
    syncCredentials: Object,
  },
  emits: ['update:modelValue', 'upstream-synced'],
  template: `<button
    type="button"
    data-testid="model-whitelist-selector"
    @click="$emit('update:modelValue', ['public-glm']); $emit('upstream-synced')"
  >models</button>`,
})

function mountModal(groups: any[] = []) {
  return mount(CreateAccountModal, {
    props: { show: true, proxies: [], groups },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        PlatformIcon: true,
        ProxySelector: true,
        ProxyAdBanner: true,
        GroupSelector: GroupSelectorStub,
        ModelWhitelistSelector: ModelWhitelistSelectorStub,
        QuotaLimitCard: true,
      },
    },
  })
}

async function selectButtonByText(wrapper: ReturnType<typeof mountModal>, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(text))
  expect(button).toBeDefined()
  await button?.trigger('click')
}

async function submitApiKeyAccount(
  platform: 'openai' | 'anthropic',
  enableLongContextBilling = false,
  disableUpstreamBillingProbe = false
) {
  const wrapper = mountModal()
  await selectButtonByText(wrapper, platform === 'openai' ? 'OpenAI' : 'admin.accounts.claudeConsole')
  if (platform === 'openai') {
    await selectButtonByText(wrapper, 'API Key')
  }
  await wrapper.get('form#create-account-form input[type="text"]').setValue(`${platform} account`)
  await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
  if (enableLongContextBilling) {
    await wrapper.get('[data-testid="openai-long-context-billing-toggle"]').trigger('click')
  }
  if (disableUpstreamBillingProbe) {
    await wrapper.get('[data-testid="upstream-billing-auto-probe"]').trigger('click')
  }
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  await flushPromises()
  return wrapper
}

async function openCodexImportStep(toggleClicks = 0) {
  const wrapper = mountModal()
  await selectButtonByText(wrapper, 'OpenAI')
  for (let click = 0; click < toggleClicks; click += 1) {
    await wrapper.get('[data-testid="openai-long-context-billing-toggle"]').trigger('click')
  }
  await wrapper.get('form#create-account-form input[type="text"]').setValue('Codex import')
  await wrapper.get('form#create-account-form').trigger('submit.prevent')
  return wrapper
}

describe('CreateAccountModal OpenAI long-context billing', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 42, platform: 'openai', type: 'apikey' })
    probeUpstreamBillingMock.mockReset().mockResolvedValue({})
    syncUpstreamModelsMock.mockReset().mockResolvedValue({ models: [], metadata: {} })
    showWarningMock.mockReset()
    checkAPIKeysDuplicateMock.mockReset().mockResolvedValue({ duplicates: [] })
    importCodexSessionMock.mockReset().mockResolvedValue({
      created: 1,
      updated: 0,
      skipped: 0,
      failed: 0,
      errors: [],
      warnings: [],
    })
    createOpenAICodexPATMock.mockReset().mockResolvedValue({})
  })

  it('hides only the redundant account toggle when every selected group enables tier pricing', async () => {
    authIsSimpleMode.value = false
    const wrapper = mountModal([
      { id: 1, long_context_pricing_enabled: true },
      { id: 2, long_context_pricing_enabled: true },
    ])

    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="select-pricing-groups"]').trigger('click')

    expect(wrapper.find('[data-testid="openai-long-context-billing-toggle"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="create-openai-ws-mode"]').exists()).toBe(true)
  })

  it('keeps the account toggle when any selected group disables tier pricing', async () => {
    authIsSimpleMode.value = false
    const wrapper = mountModal([
      { id: 1, long_context_pricing_enabled: true },
      { id: 2, long_context_pricing_enabled: false },
    ])

    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('[data-testid="select-pricing-groups"]').trigger('click')

    expect(wrapper.find('[data-testid="openai-long-context-billing-toggle"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="create-openai-ws-mode"]').exists()).toBe(true)
  })

  it('sends false explicitly for normal OpenAI account creation by default', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('omits the upstream request id header from extra when left empty', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra).not.toHaveProperty('upstream_request_id_header')
  })

  it('sends the trimmed upstream request id header in extra when filled', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('openai account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="upstream-request-id-header"]').setValue('  X-Oneapi-Request-Id  ')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.upstream_request_id_header).toBe('X-Oneapi-Request-Id')
  })

  it('omits images_url_to_b64_json from extra by default', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra).not.toHaveProperty('images_url_to_b64_json')
  })

  it('sends images_url_to_b64_json in extra when the toggle is enabled', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('openai account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="openai-images-url-to-b64-json-toggle"]').trigger('click')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.images_url_to_b64_json).toBe(true)
  })

  it('persists upstream model metadata after creating an account from preview', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('OpenCode account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="model-whitelist-selector"]').trigger('click')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledOnce()
    expect(syncUpstreamModelsMock).toHaveBeenCalledWith(42)
  })

  it('includes the current concrete model mapping in preview credentials', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="model-whitelist-selector"]').trigger('click')
    await flushPromises()

    expect(wrapper.getComponent(ModelWhitelistSelectorStub).props('syncCredentials')).toMatchObject({
      model_mapping: { 'public-glm': 'public-glm' }
    })
  })

  it('runs formal capability sync after creating an account with explicit mappings', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Mapped account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await selectButtonByText(wrapper, 'admin.accounts.modelMapping')
    await selectButtonByText(wrapper, 'admin.accounts.addMapping')
    await wrapper.get('input[placeholder="admin.accounts.requestModel"]').setValue('public-glm')
    await wrapper.get('input[placeholder="admin.accounts.actualModel"]').setValue('glm-5.3')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock.mock.calls[0]?.[0]?.credentials?.model_mapping).toEqual({
      'public-glm': 'glm-5.3'
    })
    expect(syncUpstreamModelsMock).toHaveBeenCalledWith(42)
  })

  it('warns when post-create capability metadata remains incomplete', async () => {
    syncUpstreamModelsMock.mockResolvedValue({
      models: ['x-preview-f-free'],
      warnings: [{ code: 'upstream_model_metadata_incomplete', message: 'metadata incomplete' }],
    })
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('OpenCode account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('test-api-key')
    await wrapper.get('[data-testid="model-whitelist-selector"]').trigger('click')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(showWarningMock).toHaveBeenCalledWith(
      'admin.accounts.syncUpstreamModelsMetadataIncomplete'
    )
  })

  // namespace 摊平是仅 OAuth 的兼容开关：API Key 走 chat completions 回退桥时由桥自行摊平
  it('shows the Codex namespace flatten toggle only for OpenAI OAuth accounts', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')

    expect(wrapper.find('[data-testid="create-openai-flatten-namespaces-toggle"]').exists()).toBe(
      true
    )

    await selectButtonByText(wrapper, 'API Key')
    expect(wrapper.find('[data-testid="create-openai-flatten-namespaces-toggle"]').exists()).toBe(
      false
    )
  })

  it('enables upstream billing probes by default for new OpenAI API key accounts', async () => {
    await submitApiKeyAccount('openai')

    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(true)
  })

  it('waits for the initial upstream billing probe before refreshing the account list', async () => {
    let resolveProbe: (() => void) | undefined
    probeUpstreamBillingMock.mockImplementationOnce(
      () => new Promise<void>((resolve) => {
        resolveProbe = resolve
      })
    )

    const wrapper = await submitApiKeyAccount('openai')

    expect(probeUpstreamBillingMock).toHaveBeenCalledWith(42)
    expect(wrapper.emitted('created')).toBeUndefined()

    resolveProbe?.()
    await flushPromises()

    expect(wrapper.emitted('created')).toHaveLength(1)
  })

  it('sends an explicit disabled state when the create toggle is turned off', async () => {
    await submitApiKeyAccount('openai', false, true)

    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(false)
    expect(probeUpstreamBillingMock).not.toHaveBeenCalled()
  })

  it('submits adaptive Kimi protocol endpoints', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Kimi adaptive')
    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue('sk-kimi')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://api.moonshot.cn/v1',
      api_base_urls: {
        chat_completions: 'https://api.moonshot.cn/v1',
        anthropic: 'https://api.moonshot.cn/anthropic',
        responses: 'https://api.moonshot.cn/v1'
      }
    })
  })

  it('submits adaptive Kimi Coding Plan Responses endpoint', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await selectButtonByText(wrapper, 'admin.accounts.cnProviders.accountMode.coding')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Kimi coding')
    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue('sk-kimi-coding')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'coding',
      api_protocol: 'adaptive',
      base_url: 'https://api.kimi.com/coding/v1',
      api_base_urls: {
        chat_completions: 'https://api.kimi.com/coding/v1',
        anthropic: 'https://api.kimi.com/coding',
        responses: 'https://api.kimi.com/coding/v1'
      }
    })
  })

  it('uses the edited adaptive Chat endpoint when previewing upstream models', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await wrapper
      .get('[data-testid="cn-adaptive-base-url-chat_completions"]')
      .setValue('https://relay.example.com/v1')
    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue('sk-relay')

    expect(wrapper.getComponent(ModelWhitelistSelectorStub).props('syncCredentials')).toMatchObject({
      platform: 'kimi',
      type: 'apikey',
      base_url: 'https://relay.example.com/v1',
      api_key: 'sk-relay'
    })
  })

  it('exposes Agent Identity in the OpenAI authorization methods', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('OpenAI account')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')

    const flow = wrapper.getComponent(OAuthAuthorizationFlowStub)
    expect(flow.props('showManualOption')).toBe(true)
    expect(flow.props('showCodexSessionImportOption')).toBe(true)
    expect(flow.props('showAgentIdentityOption')).toBe(true)
    expect(flow.props('showCodexPatOption')).toBe(true)
    expect(flow.props('initialInputMethod')).toBe('manual')
  })

  it.each([
    ['camelCase', { authMode: 'agentIdentity', agentIdentity: { agentRuntimeId: 'runtime' } }],
    ['nested identity without auth_mode', { agent_identity: { agent_runtime_id: 'runtime' } }],
  ])('accepts backend-compatible %s Agent Identity imports', async (_name, content) => {
    const wrapper = await openCodexImportStep()
    const flow = wrapper.getComponent(OAuthAuthorizationFlowStub)
    flow.vm.inputMethod = 'agent_identity'

    flow.vm.$emit('import-codex-session', JSON.stringify(content))
    await flushPromises()

    expect(importCodexSessionMock).toHaveBeenCalledTimes(1)
  })

  it('sends true explicitly when OpenAI long-context billing is enabled', async () => {
    await submitApiKeyAccount('openai', true)

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(true)
  })

  it('omits the OpenAI setting for non-OpenAI account creation', async () => {
    await submitApiKeyAccount('anthropic')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBeUndefined()
    // 上游倍率探测已放宽到全部 API-key 平台：非 OpenAI 平台与 OpenAI 一致，默认开启。
    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(true)
  })

  it('sends an explicit disabled state when the non-OpenAI create toggle is turned off', async () => {
    await submitApiKeyAccount('anthropic', false, true)

    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(false)
  })

  it('antigravity upstream 创建默认携带上游倍率探测开关', async () => {
    // antigravity upstream 走独立创建 helper，
    // 也必须与其余 API-key 平台一样默认开启探测并传递开关。
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Antigravity')
    await selectButtonByText(wrapper, 'admin.accounts.types.antigravityApikey')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('antigravity relay')
    const baseInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://cloudcode-pa.googleapis.com')
    expect(baseInput).toBeDefined()
    await baseInput?.setValue('https://relay.example')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-upstream')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    const payload = createAccountMock.mock.calls[0]?.[0]
    expect(payload?.platform).toBe('antigravity')
    expect(payload?.type).toBe('apikey')
    expect(payload?.upstream_billing_probe_enabled).toBe(true)
    // 创建成功后前端立即发起一次首探（与其他 apikey 平台一致）。
    expect(probeUpstreamBillingMock).toHaveBeenCalledWith(42)
  })

  it('leaves Codex session import billing ownership to the backend', async () => {
    const wrapper = await openCodexImportStep()
    await wrapper.get('[data-testid="import-codex-session"]').trigger('click')
    await flushPromises()

    expect(importCodexSessionMock).toHaveBeenCalledTimes(1)
    expect(importCodexSessionMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBeUndefined()
  })

  it('leaves Codex PAT import billing ownership to the backend', async () => {
    const wrapper = await openCodexImportStep()
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock).toHaveBeenCalledTimes(1)
    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBeUndefined()
  })

  it('sends explicit true for Codex session import after the toggle is enabled', async () => {
    const wrapper = await openCodexImportStep(1)
    await wrapper.get('[data-testid="import-codex-session"]').trigger('click')
    await flushPromises()

    expect(importCodexSessionMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(true)
  })

  it('sends explicit false for Codex session import after the toggle is changed back', async () => {
    const wrapper = await openCodexImportStep(2)
    await wrapper.get('[data-testid="import-codex-session"]').trigger('click')
    await flushPromises()

    expect(importCodexSessionMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })

  it('sends explicit true for Codex PAT import after the toggle is enabled', async () => {
    const wrapper = await openCodexImportStep(1)
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(true)
  })

  it('sends explicit false for Codex PAT import after the toggle is changed back', async () => {
    const wrapper = await openCodexImportStep(2)
    await wrapper.get('[data-testid="import-codex-pat"]').trigger('click')
    await flushPromises()

    expect(createOpenAICodexPATMock.mock.calls[0]?.[0]?.extra?.openai_long_context_billing_enabled).toBe(false)
  })
})

describe('CreateAccountModal CN provider API key batch creation', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 42, platform: 'zhipu', type: 'apikey' })
    probeUpstreamBillingMock.mockReset().mockResolvedValue({})
    syncUpstreamModelsMock.mockReset().mockResolvedValue({ models: [], metadata: {} })
    showWarningMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    checkAPIKeysDuplicateMock.mockReset().mockResolvedValue({ duplicates: [] })
  })

  async function submitCnApiKeyAccount(
    platform: 'Kimi' | 'Zhipu GLM' | 'DeepSeek',
    apiKeyInput: string,
    accountName?: string
  ) {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, platform)
    if (accountName !== undefined) {
      await wrapper.get('form#create-account-form input[type="text"]').setValue(accountName)
    }
    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue(apiKeyInput)
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    return wrapper
  }

  it('批量输入多行密钥时逐条创建并按「名称 #序号」命名（空行与首尾空白被过滤）', async () => {
    await submitCnApiKeyAccount('Zhipu GLM', 'key-one\n\n  key-two  \n', '智谱批量')

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    const [first, second] = createAccountMock.mock.calls.map((call) => call[0])
    expect(first.name).toBe('智谱批量 #1')
    expect(second.name).toBe('智谱批量 #2')
    expect(first.credentials.api_key).toBe('key-one')
    expect(second.credentials.api_key).toBe('key-two')
    // 除 api_key 外的凭据字段来自同一表单配置，逐条共享
    expect(first.credentials).toMatchObject({ account_mode: 'payg', api_protocol: 'adaptive' })
    expect(second.credentials).toMatchObject({ account_mode: 'payg', api_protocol: 'adaptive' })
    expect(showSuccessMock).toHaveBeenCalledWith('admin.accounts.oauth.batchSuccess:{"count":2}')
  })

  it.each(['Kimi', 'DeepSeek'] as const)('%s 平台同样支持一行一条批量创建', async (platform) => {
    await submitCnApiKeyAccount(platform, 'sk-a\nsk-b', `${platform} 批量`)

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe(`${platform} 批量 #1`)
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe(`${platform} 批量 #2`)
  })

  it('单条输入不加序号且仅创建一个账号', async () => {
    await submitCnApiKeyAccount('Zhipu GLM', 'only-key', '智谱单条')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('智谱单条')
    expect(showSuccessMock).toHaveBeenCalledWith('admin.accounts.accountCreated')
  })

  it('批量输入但账号名称留空时提示并阻止提交', async () => {
    await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '')

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.pleaseEnterAccountName')
  })

  it('批量输入多行密钥时渲染计数徽章与批量创建提示', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Zhipu GLM')
    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue('key-a')

    // 单条时不显示批量提示
    expect(wrapper.text()).not.toContain('admin.accounts.oauth.keysCount')
    expect(wrapper.text()).not.toContain('admin.accounts.oauth.batchCreateAccounts')

    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue('key-a\nkey-b')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keysCount:{"count":2}')
    expect(wrapper.text()).toContain('admin.accounts.oauth.batchCreateAccounts:{"count":2}')
  })

  it('输入框内重复密钥自动去重（保持首次出现顺序）', async () => {
    await submitCnApiKeyAccount('Zhipu GLM', 'dup-key\ndup-key', '重复密钥')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials.api_key).toBe('dup-key')
    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('zhipu', ['dup-key'])

    createAccountMock.mockClear()
    await submitCnApiKeyAccount('DeepSeek', 'key-b\nkey-a\nkey-b', '顺序校验')
    expect(createAccountMock.mock.calls.map((call) => call[0]?.credentials?.api_key)).toEqual(['key-b', 'key-a'])
  })

  it('全部为空行时提示输入 API Key 且不发请求', async () => {
    await submitCnApiKeyAccount('Kimi', '  \n\n \n', '空输入')

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.pleaseEnterApiKey')
  })

  it('全部失败时提示批量失败并保留弹窗与错误列表', async () => {
    createAccountMock
      .mockRejectedValueOnce({ response: { status: 500, data: { detail: 'first boom' } } })
      .mockRejectedValueOnce({ response: { status: 500, data: { detail: 'second boom' } } })

    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱全败')

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.oauth.batchFailed')
    expect(wrapper.emitted('created')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
    // 错误列表带序号与错误信息，按输入行序展示
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed:{"index":1,"error":"first boom"}')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed:{"index":2,"error":"second boom"}')
  })

  it('部分失败时提示部分成功并保留弹窗、输入与错误列表', async () => {
    createAccountMock
      .mockResolvedValueOnce({ id: 42, platform: 'zhipu', type: 'apikey' })
      .mockRejectedValueOnce({ response: { status: 500, data: { detail: 'boom' } } })

    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱混合')

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(showWarningMock).toHaveBeenCalledWith('admin.accounts.oauth.batchPartialSuccess:{"success":1,"failed":1}')
    expect(wrapper.emitted('created')).toHaveLength(1)
    // 弹窗未关闭、输入保留，便于修正失败密钥后重试
    expect(wrapper.emitted('close')).toBeUndefined()
    expect((wrapper.get('[data-testid="cn-api-keys-input"]').element as HTMLTextAreaElement).value).toBe('key-a\nkey-b')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed:{"index":2,"error":"boom"}')
  })

  it('失败后重新提交成功会清空错误列表', async () => {
    createAccountMock.mockRejectedValueOnce({ response: { status: 500, data: { detail: 'boom' } } })
    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱重试')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed')

    // 重新提交全部成功：错误列表被清空，弹窗关闭
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(wrapper.text()).not.toContain('admin.accounts.oauth.keyAuthFailed')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('切换平台后批量错误列表不再展示', async () => {
    createAccountMock.mockRejectedValueOnce({ response: { status: 500, data: { detail: 'boom' } } })
    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱残留')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed')

    await selectButtonByText(wrapper, 'Kimi')

    expect(wrapper.text()).not.toContain('admin.accounts.oauth.keyAuthFailed')
  })

  it('单条创建命中库内重复时阻止提交并提示所属账号', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [{ api_key: 'key-a', account_id: 7, account_name: '智谱已有' }]
    })

    await submitCnApiKeyAccount('Zhipu GLM', 'key-a', '智谱单条')

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.apiKeyExists:{"name":"智谱已有"}')
  })

  it('批量创建跳过库内重复密钥并重排序号创建其余', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [{ api_key: 'key-old', account_id: 9, account_name: '智谱已有' }]
    })

    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-old\nkey-new-1\nkey-new-2', '智谱批量')

    // 查重请求携带全部输入密钥；仅创建未重复的两条并重排序号
    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('zhipu', ['key-old', 'key-new-1', 'key-new-2'])
    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('智谱批量 #1')
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials.api_key).toBe('key-new-1')
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe('智谱批量 #2')
    expect(createAccountMock.mock.calls[1]?.[0]?.credentials.api_key).toBe('key-new-2')
    // 全部成功但有跳过：警告汇总 + 保留弹窗展示跳过明细
    expect(showWarningMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.batchSuccessWithSkipped:{"count":2}')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.text()).toContain('admin.accounts.duplicateCheck.skippedKeys:{"count":1}')
    // 跳过明细中的密钥以掩码展示，输入框之外不持久化完整明文
    expect(wrapper.text()).toContain('ke****（智谱已有）')
    expect(wrapper.text()).not.toContain('key-old（智谱已有）')
  })

  it('批量输入全部与库内重复时不创建任何账号', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [
        { api_key: 'key-a', account_id: 1, account_name: '账号一' },
        { api_key: 'key-b', account_id: 2, account_name: '账号二' }
      ]
    })

    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱全重')

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.allKeysExist')
    expect(wrapper.text()).toContain('admin.accounts.duplicateCheck.skippedKeys:{"count":2}')
  })

  it('查重服务不可用时阻止提交', async () => {
    checkAPIKeysDuplicateMock.mockRejectedValueOnce(new Error('network down'))

    await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱查重失败')

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.apiKeyCheckFailed')
  })

  it('非国产平台单条创建同样查重（命中即阻止）', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [{ api_key: 'sk-test-api-key', account_id: 5, account_name: '已有 OpenAI 账号' }]
    })

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('openai account')
    await wrapper.get('form#create-account-form input[type="password"]').setValue('sk-test-api-key')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('openai', ['sk-test-api-key'])
    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith(
      'admin.accounts.duplicateCheck.apiKeyExists:{"name":"已有 OpenAI 账号"}'
    )
  })

  it('非国产平台仍使用单行密码输入（回归保护）', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'admin.accounts.claudeConsole')

    expect(wrapper.find('[data-testid="cn-api-keys-input"]').exists()).toBe(false)
    expect(wrapper.find('form#create-account-form input[type="password"]').exists()).toBe(true)
  })

  it('批量输入查重后仅剩一条时仍按批量语义命名（名称 #1）', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [{ api_key: 'key-old', account_id: 9, account_name: '智谱已有' }]
    })

    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-old\nkey-new', '智谱剩一条')

    // 原始输入为批量（2 条）：即使查重后仅剩 1 条可创建，也保持 #1 命名与批量提示
    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('智谱剩一条 #1')
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials.api_key).toBe('key-new')
    expect(showWarningMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.batchSuccessWithSkipped:{"count":1}')
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('查重进行中重复触发提交会被忽略（不可重入）', async () => {
    // 受控 promise：查重挂起期间再次提交应被 submitting 守卫拦截
    let resolveCheck: ((value: { duplicates: never[] }) => void) | undefined
    checkAPIKeysDuplicateMock.mockImplementationOnce(
      () => new Promise((resolve) => { resolveCheck = resolve })
    )

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Zhipu GLM')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('防重入')
    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue('key-a\nkey-b')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    // 查重挂起中再次触发提交：守卫直接忽略，不发起第二次查重
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledTimes(1)

    resolveCheck?.({ duplicates: [] })
    await flushPromises()

    // 仅一次创建流程，两条密钥各创建一次
    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('防重入 #1')
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe('防重入 #2')
  })

  it('查重进行中切换平台后旧请求恢复不再创建', async () => {
    let resolveCheck: ((value: { duplicates: never[] }) => void) | undefined
    checkAPIKeysDuplicateMock.mockImplementationOnce(
      () => new Promise((resolve) => { resolveCheck = resolve })
    )

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Zhipu GLM')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('切平台')
    await wrapper.get('[data-testid="cn-api-keys-input"]').setValue('key-a\nkey-b')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    // 查重挂起中切到 Kimi：提交序号失效
    await selectButtonByText(wrapper, 'Kimi')

    resolveCheck?.({ duplicates: [] })
    await flushPromises()

    // 旧请求恢复后不得按新平台（或任何平台）创建账号
    expect(createAccountMock).not.toHaveBeenCalled()
  })

  it('重新提交在本地校验失败时也清空上一轮反馈状态', async () => {
    // 第一轮：批量部分失败，留下错误列表
    createAccountMock.mockRejectedValueOnce({ response: { status: 500, data: { detail: 'boom' } } })
    const wrapper = await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱清理')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed')

    // 第二轮：清空名称后提交（多条名称必填校验失败，在任何网络请求前 return）
    await wrapper.get('form#create-account-form input[type="text"]').setValue('')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.pleaseEnterAccountName')
    // 上一轮的错误列表已被提交入口清空，不再残留旧密钥信息
    expect(wrapper.text()).not.toContain('admin.accounts.oauth.keyAuthFailed')
  })
})
