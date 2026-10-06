import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import TempUnschedSetModal from '../TempUnschedSetModal.vue'
import type { Account } from '@/types'

const mocks = vi.hoisted(() => ({
  setTempUnschedulable: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())

const account = { id: 7, name: 'acc-7' } as Account

async function open() {
  const w = mount(TempUnschedSetModal, {
    props: { show: false, account },
    global: {
      stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } }
    }
  })
  await w.setProps({ show: true })
  return w
}

function submitButton(w: Awaited<ReturnType<typeof open>>) {
  return w.findAll('button').find(b => b.attributes('data-testid') === 'temp-unsched-submit')!
}

describe('TempUnschedSetModal', () => {
  it('disables submit for out-of-range durations and enables within range', async () => {
    const w = await open()
    const input = w.get('[data-testid="temp-unsched-duration-input"]')
    const submit = submitButton(w)

    expect(submit.attributes('disabled')).toBeUndefined()

    await input.setValue('0')
    expect(submit.attributes('disabled')).toBeDefined()

    await input.setValue('10081')
    expect(submit.attributes('disabled')).toBeDefined()

    await input.setValue('30.5')
    expect(submit.attributes('disabled')).toBeDefined()

    await input.setValue('45')
    expect(submit.attributes('disabled')).toBeUndefined()
  })

  it('preset buttons fill the duration', async () => {
    const w = await open()
    // i18n mock 返回原始 key，前四个按钮依次对应预设 15/30/60/120
    const presetButtons = w.findAll('button').filter(b => b.attributes('type') !== 'submit')
    const preset60 = presetButtons[2]
    await preset60.trigger('click')
    expect((w.get('[data-testid="temp-unsched-duration-input"]').element as HTMLInputElement).value).toBe('60')
  })

  it('submits duration and trimmed reason, emits set with updated account', async () => {
    const updated = { ...account, temp_unschedulable_until: '2099-01-01T00:00:00Z' } as Account
    mocks.setTempUnschedulable.mockResolvedValueOnce(updated)
    const w = await open()
    await w.get('[data-testid="temp-unsched-duration-input"]').setValue('30')
    await w.get('[data-testid="temp-unsched-reason-input"]').setValue('  维护  ')
    await submitButton(w).trigger('click')
    await flushPromises()

    expect(mocks.setTempUnschedulable).toHaveBeenCalledWith(7, 30, '维护')
    expect(mocks.showSuccess).toHaveBeenCalled()
    expect(w.emitted('set')?.[0]).toEqual([updated])
    expect(w.emitted('close')).toBeTruthy()
  })

  it('shows error and keeps the dialog open on failure', async () => {
    mocks.setTempUnschedulable.mockRejectedValueOnce(new Error('boom'))
    const w = await open()
    await submitButton(w).trigger('click')
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalled()
    expect(w.emitted('set')).toBeUndefined()
    expect(w.emitted('close')).toBeUndefined()
  })

  it('resets the form when reopened', async () => {
    const w = await open()
    await w.get('[data-testid="temp-unsched-reason-input"]').setValue('note')
    await w.setProps({ show: false })
    await w.setProps({ show: true })
    expect((w.get('[data-testid="temp-unsched-reason-input"]').element as HTMLInputElement).value).toBe('')
    expect((w.get('[data-testid="temp-unsched-duration-input"]').element as HTMLInputElement).value).toBe('30')
  })
})
