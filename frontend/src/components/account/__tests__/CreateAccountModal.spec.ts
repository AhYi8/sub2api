import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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
  checkMixedChannelRiskMock,
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
  checkMixedChannelRiskMock: vi.fn(),
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
      checkMixedChannelRisk: checkMixedChannelRiskMock,
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
  await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
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
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    checkAPIKeysDuplicateMock.mockReset().mockResolvedValue({ duplicates: [] })
    // anthropic/antigravity 创建都会做 mixed-channel 检查，默认无风险
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
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

  afterEach(() => vi.useRealTimers())

  it('sets month and year expiry presets without submitting the account form', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-01-31T12:34:00'))
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('expiry account')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
    const input = wrapper.get<HTMLInputElement>('input[type="datetime-local"]')

    for (const [label, expected] of [
      ['payment.oneMonth', '2026-02-28T12:34'],
      ['payment.oneYear', '2027-01-31T12:34'],
    ]) {
      const button = wrapper.findAll('button').find((candidate) => candidate.text() === label)!
      expect(button.attributes('type')).toBe('button')
      await button.trigger('click')
      expect(input.element.value).toBe(expected)
      expect(createAccountMock).not.toHaveBeenCalled()
    }

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(createAccountMock.mock.calls[0]?.[0]?.expires_at).toBe(new Date('2027-01-31T12:34:00').getTime() / 1000)
    wrapper.unmount()
  })

  it('allows a manually entered expiry to override a preset before account creation', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('custom expiry account')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
    await selectButtonByText(wrapper, 'payment.oneMonth')
    await wrapper.get('input[type="datetime-local"]').setValue('2030-04-15T09:20')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(createAccountMock.mock.calls[0]?.[0]?.expires_at).toBe(new Date('2030-04-15T09:20:00').getTime() / 1000)
    wrapper.unmount()
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('test-api-key')
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

  it('submits OpenCode Zen default protocol rules with adaptive endpoints', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenCode')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('oc')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('sk-opencode-zen')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'zen',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/v1',
      api_base_urls: {
        chat_completions: 'https://opencode.ai/zen/v1',
        anthropic: 'https://opencode.ai/zen',
        responses: 'https://opencode.ai/zen/v1'
      },
      protocol_rules: [
        { pattern: 'grok-*', protocol: 'responses' },
        { pattern: 'gpt-*', protocol: 'responses' },
        { pattern: 'muse-spark-*', protocol: 'responses' },
        { pattern: 'claude-*', protocol: 'anthropic' },
        { pattern: 'qwen*', protocol: 'anthropic' }
      ]
    })
  })

  it('submits OpenCode GO endpoints after switching account type', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenCode')
    await selectButtonByText(wrapper, 'admin.accounts.opencodeGo.accountMode.go')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('oc-go')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('sk-opencode-go')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'go',
      api_protocol: 'adaptive',
      base_url: 'https://opencode.ai/zen/go/v1',
      api_base_urls: {
        chat_completions: 'https://opencode.ai/zen/go/v1',
        anthropic: 'https://opencode.ai/zen/go',
        responses: 'https://opencode.ai/zen/go/v1'
      },
      protocol_rules: [
        { pattern: 'grok-*', protocol: 'responses' },
        { pattern: 'gpt-*', protocol: 'responses' },
        { pattern: 'muse-spark-*', protocol: 'responses' },
        { pattern: 'minimax-*', protocol: 'anthropic' },
        { pattern: 'qwen*', protocol: 'anthropic' }
      ]
    })
  })

  it('submits adaptive Kimi protocol endpoints', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('Kimi adaptive')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('sk-kimi')

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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('sk-kimi-coding')

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

  it('submits adaptive MiniMax protocol endpoints', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'MiniMax')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('MiniMax adaptive')
    // MiniMax 归入国产平台分组（isCNProviderPlatform 含 minimax），继承多行批量
    // API Key 输入（fork 功能对上游新国产平台的自然扩展），不再是 password 输入框。
    await wrapper.get('[data-testid="api-keys-input"]').setValue('sk-minimax')

    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      account_mode: 'payg',
      api_protocol: 'adaptive',
      base_url: 'https://api.minimaxi.com/v1',
      api_base_urls: {
        chat_completions: 'https://api.minimaxi.com/v1',
        anthropic: 'https://api.minimaxi.com/anthropic',
        responses: 'https://api.minimaxi.com/v1'
      }
    })
  })

  it('uses the edited adaptive Chat endpoint when previewing upstream models', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Kimi')
    await wrapper
      .get('[data-testid="cn-adaptive-base-url-chat_completions"]')
      .setValue('https://relay.example.com/v1')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('sk-relay')

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
    await wrapper.get('[data-testid="antigravity-upstream-api-keys-input"]').setValue('sk-upstream')
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue(apiKeyInput)
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('key-a')

    // 单条时不显示批量提示
    expect(wrapper.text()).not.toContain('admin.accounts.oauth.keysCount')
    expect(wrapper.text()).not.toContain('admin.accounts.oauth.batchCreateAccounts')

    await wrapper.get('[data-testid="api-keys-input"]').setValue('key-a\nkey-b')
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
    expect((wrapper.get('[data-testid="api-keys-input"]').element as HTMLTextAreaElement).value).toBe('key-a\nkey-b')
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

  it('大批量输入（超过 200 条）不再被条数限制拦截，正常发起查重并逐条创建', async () => {
    // 查重已与批量创建流程对齐：不限制条数（后端仅限单条长度与请求体大小），
    // 历史上 binding max=200 曾把这类请求 400 拒绝并被误报为"查重服务不可用"
    const many = Array.from({ length: 201 }, (_, i) => `key-${i}`).join('\n')

    await submitCnApiKeyAccount('DeepSeek', many, '大批量')

    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledTimes(1)
    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('deepseek', Array.from({ length: 201 }, (_, i) => `key-${i}`))
    expect(createAccountMock).toHaveBeenCalledTimes(201)
  })

  it('查重请求被后端 400 拒绝时提示输入问题而非服务不可用', async () => {
    // apiClient 拦截器 reject 平铺错误对象（{ status, message }）
    checkAPIKeysDuplicateMock.mockRejectedValueOnce({ status: 400, message: 'Invalid request' })

    await submitCnApiKeyAccount('Zhipu GLM', 'key-a\nkey-b', '智谱被拒')

    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.apiKeyCheckRejected')
  })

  it('非国产平台单条创建同样查重（命中即阻止）', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [{ api_key: 'sk-test-api-key', account_id: 5, account_name: '已有 OpenAI 账号' }]
    })

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenAI')
    await selectButtonByText(wrapper, 'API Key')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('openai account')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('sk-test-api-key')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('openai', ['sk-test-api-key'])
    expect(createAccountMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith(
      'admin.accounts.duplicateCheck.apiKeyExists:{"name":"已有 OpenAI 账号"}'
    )
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('key-a\nkey-b')
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
    await wrapper.get('[data-testid="api-keys-input"]').setValue('key-a\nkey-b')
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

// 批量创建能力从国产平台推广到全部 apikey 型入口：
// 通用 apikey 单行输入（anthropic/openai/gemini/grok/opencode_go）与 antigravity upstream。
describe('CreateAccountModal all-platform apikey batch creation', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 42, platform: 'anthropic', type: 'apikey' })
    probeUpstreamBillingMock.mockReset().mockResolvedValue({})
    syncUpstreamModelsMock.mockReset().mockResolvedValue({ models: [], metadata: {} })
    showWarningMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    checkAPIKeysDuplicateMock.mockReset().mockResolvedValue({ duplicates: [] })
    // anthropic 创建会做 mixed-channel 检查，默认无风险
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  // 进入各平台 apikey 表单（不提交）：不同平台的入口按钮不同（openai/grok 需再点 API Key 分类）
  async function openPlatformApiKeyForm(
    platform: 'anthropic' | 'openai' | 'gemini' | 'grok' | 'opencode_go',
    apiKeyInput: string,
    accountName: string
  ) {
    const wrapper = mountModal()
    const platformButton = {
      anthropic: 'admin.accounts.claudeConsole',
      openai: 'OpenAI',
      gemini: 'Gemini',
      grok: 'Grok',
      opencode_go: 'OpenCode',
    }[platform]
    await selectButtonByText(wrapper, platformButton)
    if (platform === 'openai' || platform === 'grok') {
      await selectButtonByText(wrapper, 'API Key')
    } else if (platform === 'gemini') {
      await selectButtonByText(wrapper, 'admin.accounts.gemini.accountType.apiKeyTitle')
    }
    await wrapper.get('form#create-account-form input[type="text"]').setValue(accountName)
    await wrapper.get('[data-testid="api-keys-input"]').setValue(apiKeyInput)
    return wrapper
  }

  async function submitPlatformApiKeyBatch(
    platform: 'anthropic' | 'openai' | 'gemini' | 'grok' | 'opencode_go',
    apiKeyInput: string,
    accountName: string
  ) {
    const wrapper = await openPlatformApiKeyForm(platform, apiKeyInput, accountName)
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    return wrapper
  }

  it.each([
    ['anthropic', 'anthropic', 'https://api.anthropic.com'],
    ['openai', 'openai', 'https://api.openai.com'],
    ['gemini', 'gemini', 'https://generativelanguage.googleapis.com'],
    ['grok', 'grok', 'https://api.x.ai/v1'],
  ] as const)('%s 平台多行密钥批量创建：查重带平台、逐条创建、名称 #序号', async (platform, expectedPlatform, expectedBaseUrl) => {
    await submitPlatformApiKeyBatch(platform, 'key-a\nkey-b', `${platform} 批量`)

    // 查重按平台发起；批量创建逐条提交并按「名称 #序号」命名
    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith(expectedPlatform, ['key-a', 'key-b'])
    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe(`${platform} 批量 #1`)
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe(`${platform} 批量 #2`)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials.api_key).toBe('key-a')
    expect(createAccountMock.mock.calls[1]?.[0]?.credentials.api_key).toBe('key-b')
    // 除 api_key 外的凭据字段（base_url 等）来自同一表单配置，逐条共享
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials.base_url).toBe(expectedBaseUrl)
    expect(showSuccessMock).toHaveBeenCalledWith('admin.accounts.oauth.batchSuccess:{"count":2}')
  })

  it('opencode_go 平台多行密钥批量创建：逐条创建并共享多协议端点配置', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'OpenCode')
    await selectButtonByText(wrapper, 'admin.accounts.opencodeGo.accountMode.go')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('opencode 批量')
    await wrapper.get('[data-testid="api-keys-input"]').setValue('key-a\nkey-b')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('opencode_go', ['key-a', 'key-b'])
    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('opencode 批量 #1')
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe('opencode 批量 #2')
    // 共享的账号模式与多协议端点配置随每条账号继承
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      api_key: 'key-a',
      account_mode: 'go',
      api_protocol: 'adaptive'
    })
  })

  it('全部 apikey 平台统一使用多行密钥输入（回归保护）', async () => {
    const wrapper = await openPlatformApiKeyForm('anthropic', 'only-key', '回归保护')

    // 批量能力已推广到全部 apikey 平台：单行密码输入框不再存在（bedrock 专属字段除外）
    expect(wrapper.find('[data-testid="api-keys-input"]').exists()).toBe(true)
    expect(wrapper.find('form#create-account-form input[type="password"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.apiKeyBatchHint')
  })

  it.each(['anthropic', 'openai', 'gemini', 'grok', 'opencode_go'] as const)(
    '%s 平台多行输入渲染计数徽章与批量提示',
    async (platform) => {
      const wrapper = await openPlatformApiKeyForm(platform, 'key-a\nkey-b', '计数')

      expect(wrapper.text()).toContain('admin.accounts.oauth.keysCount:{"count":2}')
      expect(wrapper.text()).toContain('admin.accounts.oauth.batchCreateAccounts:{"count":2}')
      expect(wrapper.text()).toContain('admin.accounts.apiKeyBatchHint')
    }
  )

  it('非 CN 平台批量创建查重命中时跳过重复行并继续创建其余', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [{ api_key: 'key-old', account_id: 3, account_name: '已有 Anthropic 账号' }]
    })

    const wrapper = await submitPlatformApiKeyBatch('anthropic', 'key-old\nkey-new', '查重跳过')

    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('anthropic', ['key-old', 'key-new'])
    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials.api_key).toBe('key-new')
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('查重跳过 #1')
    expect(showWarningMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.batchSuccessWithSkipped:{"count":1}')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.text()).toContain('admin.accounts.duplicateCheck.skippedKeys:{"count":1}')
  })

  it('非 CN 平台批量部分失败时保留弹窗与错误列表', async () => {
    createAccountMock
      .mockResolvedValueOnce({ id: 42, platform: 'openai', type: 'apikey' })
      .mockRejectedValueOnce({ response: { status: 500, data: { detail: 'boom' } } })

    const wrapper = await submitPlatformApiKeyBatch('openai', 'key-a\nkey-b', '部分失败')

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(showWarningMock).toHaveBeenCalledWith('admin.accounts.oauth.batchPartialSuccess:{"success":1,"failed":1}')
    expect(wrapper.emitted('created')).toHaveLength(1)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect((wrapper.get('[data-testid="api-keys-input"]').element as HTMLTextAreaElement).value).toBe('key-a\nkey-b')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed:{"index":2,"error":"boom"}')
  })
})

describe('CreateAccountModal antigravity upstream batch creation', () => {
  beforeEach(() => {
    authIsSimpleMode.value = true
    createAccountMock.mockReset().mockResolvedValue({ id: 42, platform: 'antigravity', type: 'apikey' })
    probeUpstreamBillingMock.mockReset().mockResolvedValue({})
    syncUpstreamModelsMock.mockReset().mockResolvedValue({ models: [], metadata: {} })
    showWarningMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    checkAPIKeysDuplicateMock.mockReset().mockResolvedValue({ duplicates: [] })
    checkMixedChannelRiskMock.mockReset().mockResolvedValue({ has_risk: false })
  })

  async function submitUpstreamBatch(apiKeyInput: string, accountName: string) {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Antigravity')
    await selectButtonByText(wrapper, 'admin.accounts.types.antigravityApikey')
    await wrapper.get('form#create-account-form input[type="text"]').setValue(accountName)
    const baseInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://cloudcode-pa.googleapis.com')
    expect(baseInput).toBeDefined()
    await baseInput?.setValue('https://relay.example')
    await wrapper.get('[data-testid="antigravity-upstream-api-keys-input"]').setValue(apiKeyInput)
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    return wrapper
  }

  it('多行 API Key 批量创建：查重带平台、逐条创建、共享 base_url、mixed-channel 仅检查一次', async () => {
    await submitUpstreamBatch('sk-a\nsk-b', '上游批量')

    // 查重按 antigravity 平台发起，重复行跳过逻辑与通用路径一致
    expect(checkAPIKeysDuplicateMock).toHaveBeenCalledWith('antigravity', ['sk-a', 'sk-b'])
    // mixed-channel 风险基于 platform × group_ids，与具体密钥无关：批量只检查一次
    expect(checkMixedChannelRiskMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('上游批量 #1')
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe('上游批量 #2')
    // 共享 base_url 逐条继承；api_key 逐条差异
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      api_key: 'sk-a',
      base_url: 'https://relay.example'
    })
    expect(createAccountMock.mock.calls[1]?.[0]?.credentials).toMatchObject({
      api_key: 'sk-b',
      base_url: 'https://relay.example'
    })
    // upstream 也是 API-key 账号：每条都发起上游倍率首探
    expect(createAccountMock.mock.calls[0]?.[0]?.upstream_billing_probe_enabled).toBe(true)
    expect(probeUpstreamBillingMock).toHaveBeenCalledTimes(2)
    expect(showSuccessMock).toHaveBeenCalledWith('admin.accounts.oauth.batchSuccess:{"count":2}')
  })

  it('单条输入保持既有路径：不加序号且 mixed-channel 检查一次', async () => {
    await submitUpstreamBatch('sk-only', '上游单条')

    expect(checkMixedChannelRiskMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('上游单条')
    expect(showSuccessMock).toHaveBeenCalledWith('admin.accounts.accountCreated')
  })

  it('部分失败时提示部分成功并保留弹窗、输入与错误列表', async () => {
    createAccountMock
      .mockResolvedValueOnce({ id: 42, platform: 'antigravity', type: 'apikey' })
      .mockRejectedValueOnce({ response: { status: 500, data: { detail: 'boom' } } })

    const wrapper = await submitUpstreamBatch('sk-a\nsk-b', '上游混合')

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(showWarningMock).toHaveBeenCalledWith('admin.accounts.oauth.batchPartialSuccess:{"success":1,"failed":1}')
    expect(wrapper.emitted('created')).toHaveLength(1)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect((wrapper.get('[data-testid="antigravity-upstream-api-keys-input"]').element as HTMLTextAreaElement).value).toBe('sk-a\nsk-b')
    expect(wrapper.text()).toContain('admin.accounts.oauth.keyAuthFailed:{"index":2,"error":"boom"}')
  })

  it('查重命中跳过重复行后创建其余并展示跳过明细', async () => {
    checkAPIKeysDuplicateMock.mockResolvedValueOnce({
      duplicates: [{ api_key: 'sk-old', account_id: 8, account_name: '已有上游账号' }]
    })

    const wrapper = await submitUpstreamBatch('sk-old\nsk-new', '上游查重')

    expect(createAccountMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials.api_key).toBe('sk-new')
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('上游查重 #1')
    expect(showWarningMock).toHaveBeenCalledWith('admin.accounts.duplicateCheck.batchSuccessWithSkipped:{"count":1}')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.text()).toContain('admin.accounts.duplicateCheck.skippedKeys:{"count":1}')
  })

  it('mixed-channel 有风险时：确认前不创建，确认后逐条创建并携带确认标志', async () => {
    checkMixedChannelRiskMock.mockResolvedValueOnce({ has_risk: true })

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Antigravity')
    await selectButtonByText(wrapper, 'admin.accounts.types.antigravityApikey')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('风险确认')
    const baseInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://cloudcode-pa.googleapis.com')
    await baseInput?.setValue('https://relay.example')
    await wrapper.get('[data-testid="antigravity-upstream-api-keys-input"]').setValue('sk-a\nsk-b')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    // 确认前：只完成一次风险检查，未创建任何账号
    expect(checkMixedChannelRiskMock).toHaveBeenCalledTimes(1)
    expect(createAccountMock).not.toHaveBeenCalled()

    // 用户确认风险后恢复批量流程，每条创建都带确认标志
    wrapper.getComponent({ name: 'ConfirmDialog' }).vm.$emit('confirm')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.confirm_mixed_channel_risk).toBe(true)
    expect(createAccountMock.mock.calls[1]?.[0]?.confirm_mixed_channel_risk).toBe(true)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('风险确认 #1')
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe('风险确认 #2')
  })

  it('批量创建逐条继承临时不可调度规则（与单条路径一致）', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Antigravity')
    await selectButtonByText(wrapper, 'admin.accounts.types.antigravityApikey')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('规则批量')
    const baseInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://cloudcode-pa.googleapis.com')
    await baseInput?.setValue('https://relay.example')

    // 开启临时不可调度并添加一条 429 预设规则（开关为区块标题旁的无文本 toggle）
    const toggle = wrapper
      .findAll('div.mb-3')
      .find((container) => container.text().includes('admin.accounts.tempUnschedulable.title'))
      ?.find('button')
    expect(toggle).toBeDefined()
    await toggle?.trigger('click')
    await selectButtonByText(wrapper, '+ admin.accounts.tempUnschedulable.presets.rateLimitLabel')

    await wrapper.get('[data-testid="antigravity-upstream-api-keys-input"]').setValue('sk-a\nsk-b')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    // 共享凭据构造阶段写入的规则被逐条 payload 继承，不因批量而丢失
    expect(createAccountMock.mock.calls[0]?.[0]?.credentials).toMatchObject({
      temp_unschedulable_enabled: true,
      temp_unschedulable_rules: [{ error_code: 429 }]
    })
    expect(createAccountMock.mock.calls[1]?.[0]?.credentials).toMatchObject({
      temp_unschedulable_enabled: true
    })
  })

  it('批量创建逐条携带配额限制（与单条路径一致）', async () => {
    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Antigravity')
    await selectButtonByText(wrapper, 'admin.accounts.types.antigravityApikey')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('配额批量')
    const baseInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://cloudcode-pa.googleapis.com')
    await baseInput?.setValue('https://relay.example')

    // QuotaLimitCard 被 stub，但父级事件监听仍生效：直接 emit 更新配额上限
    wrapper.getComponent({ name: 'QuotaLimitCard' }).vm.$emit('update:totalLimit', 100)

    await wrapper.get('[data-testid="antigravity-upstream-api-keys-input"]').setValue('sk-a\nsk-b')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    expect(createAccountMock).toHaveBeenCalledTimes(2)
    // 配额注入与单条路径共用同一 helper，批量不丢弃
    expect(createAccountMock.mock.calls[0]?.[0]?.extra?.quota_limit).toBe(100)
    expect(createAccountMock.mock.calls[1]?.[0]?.extra?.quota_limit).toBe(100)
  })

  it('mixed-channel 检查挂起期间的重复提交被防重入拦截', async () => {
    // 受控 promise：查重完成后 mixed-channel 检查挂起，期间 submitting 占位须挡住第二次提交
    let resolveRisk: ((value: { has_risk: boolean }) => void) | undefined
    checkMixedChannelRiskMock.mockImplementationOnce(
      () => new Promise((resolve) => { resolveRisk = resolve })
    )

    const wrapper = mountModal()
    await selectButtonByText(wrapper, 'Antigravity')
    await selectButtonByText(wrapper, 'admin.accounts.types.antigravityApikey')
    await wrapper.get('form#create-account-form input[type="text"]').setValue('防重入')
    const baseInput = wrapper
      .findAll('input')
      .find((candidate) => candidate.attributes('placeholder') === 'https://cloudcode-pa.googleapis.com')
    await baseInput?.setValue('https://relay.example')
    await wrapper.get('[data-testid="antigravity-upstream-api-keys-input"]').setValue('sk-a\nsk-b')
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()

    // 检查挂起中再次触发提交：分支入口的 submitting 守卫直接忽略
    await wrapper.get('form#create-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(checkMixedChannelRiskMock).toHaveBeenCalledTimes(1)

    resolveRisk?.({ has_risk: false })
    await flushPromises()

    // 仅一轮批量流程，两条密钥各创建一次，不因并发提交重复建号
    expect(createAccountMock).toHaveBeenCalledTimes(2)
    expect(createAccountMock.mock.calls[0]?.[0]?.name).toBe('防重入 #1')
    expect(createAccountMock.mock.calls[1]?.[0]?.name).toBe('防重入 #2')
  })
})
