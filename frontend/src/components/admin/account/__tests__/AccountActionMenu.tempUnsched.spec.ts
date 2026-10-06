import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const account = {
  id: 1,
  platform: 'anthropic',
  type: 'oauth',
  status: 'active'
} as Account

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class {
    observe = vi.fn()
    disconnect = vi.fn()
    unobserve = vi.fn()
  })
})
afterEach(() => vi.unstubAllGlobals())

async function mountMenu(acc: Account = account) {
  const wrapper = mount(AccountActionMenu, {
    props: { show: true, account: acc, anchorRect: new DOMRect(100, 100, 32, 24) },
    global: { stubs: { Icon: true } },
    attachTo: document.body
  })
  await flushPromises()
  return wrapper
}

// 菜单内容 Teleport 到 body，需从 document 查询
function findMenuButton(label: string) {
  const buttons = Array.from(document.body.querySelectorAll<HTMLElement>('.action-menu-content button'))
  return buttons.find(b => b.textContent?.includes(label))
}

describe('AccountActionMenu temp-unsched action', () => {
  it('shows the manual temp-unschedulable entry for a regular account and emits temp-unsched', async () => {
    const wrapper = await mountMenu()

    const button = findMenuButton('admin.accounts.tempUnschedulable.menuLabel')
    expect(button).toBeDefined()
    button!.click()
    await flushPromises()

    expect(wrapper.emitted('temp-unsched')).toEqual([[account]])
    expect(wrapper.emitted('close')).toBeTruthy()
  })

  it('shows the entry for an already temp-unschedulable account (override semantics)', async () => {
    const paused = { ...account, temp_unschedulable_until: '2099-01-01T00:00:00Z' } as Account
    await mountMenu(paused)

    const button = findMenuButton('admin.accounts.tempUnschedulable.menuLabel')
    expect(button).toBeDefined()
  })
})
