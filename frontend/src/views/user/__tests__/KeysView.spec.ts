import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { ApiKey } from '@/types'
import KeysView from '../KeysView.vue'

const keysViewSource = readFileSync(
  resolve(dirname(fileURLToPath(import.meta.url)), '../KeysView.vue'),
  'utf8'
)

const {
  listKeys,
  createKey,
  updateKey,
  toggleKeyStatus,
  getPublicSettings,
  getDashboardApiKeysUsage,
  getAvailableGroups,
  getUserGroupRates,
  getSecurityDepositEligibility,
  showError,
  showSuccess,
  copyToClipboard,
  isCurrentStep,
  nextStep,
} = vi.hoisted(() => ({
  listKeys: vi.fn(),
  createKey: vi.fn(),
  updateKey: vi.fn(),
  toggleKeyStatus: vi.fn(),
  getPublicSettings: vi.fn(),
  getDashboardApiKeysUsage: vi.fn(),
  getAvailableGroups: vi.fn(),
  getUserGroupRates: vi.fn(),
  getSecurityDepositEligibility: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  copyToClipboard: vi.fn(),
  isCurrentStep: vi.fn(),
  nextStep: vi.fn(),
}))

const messages: Record<string, string> = {
  'common.actions': 'Actions',
  'common.name': 'Name',
  'common.refresh': 'Refresh',
  'common.status': 'Status',
  'keys.apiKey': 'API Key',
  'keys.allGroups': 'All Groups',
  'keys.allStatus': 'All Status',
  'keys.columnSettings': 'Column Settings',
  'keys.createKey': 'Create API Key',
  'keys.created': 'Created',
  'keys.expiresAt': 'Expires',
  'keys.group': 'Group',
  'keys.id': 'ID',
  'keys.currentConcurrency': 'Current Concurrency',
  'keys.lastUsedAt': 'Last Used',
  'keys.lastUsedIP': 'Last Used IP',
  'keys.rateLimitColumn': 'Rate Limit',
  'keys.searchPlaceholder': 'Search name or key...',
  'keys.status.active': 'Active',
  'keys.status.expired': 'Expired',
  'keys.status.inactive': 'Inactive',
  'keys.status.quota_exhausted': 'Quota exhausted',
  'keys.usage': 'Usage',
}

vi.mock('@/api', () => ({
  keysAPI: {
    list: listKeys,
    create: createKey,
    update: updateKey,
    delete: vi.fn(),
    toggleStatus: toggleKeyStatus,
  },
  authAPI: {
    getPublicSettings,
  },
  usageAPI: {
    getDashboardApiKeysUsage,
  },
  userGroupsAPI: {
    getAvailable: getAvailableGroups,
    getUserGroupRates,
  },
  securityDepositsAPI: {
    getEligibility: getSecurityDepositEligibility,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep,
    nextStep,
  }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const createApiKey = (): ApiKey => ({
  id: 1,
  user_id: 1,
  key: 'sk-test-key',
  name: 'test-key',
  group_id: null,
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  last_used_ip: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-06-27T00:00:00Z',
  updated_at: '2026-06-27T00:00:00Z',
  current_concurrency: 3,
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
})

const AppLayoutStub = {
  template: '<div><slot /></div>',
}

const TablePageLayoutStub = {
  template: `
    <div>
      <slot name="filters" />
      <slot name="actions" />
      <slot name="table" />
      <slot name="pagination" />
    </div>
  `,
}

const DataTableStub = {
  name: 'DataTable',
  props: ['columns', 'data'],
  emits: ['sort'],
  template: `
    <div>
      <div data-test="columns">{{ columns.map((col) => col.key).join(',') }}</div>
      <div data-test="columns-meta">{{ JSON.stringify(columns.map((col) => ({ key: col.key, sortable: !!col.sortable }))) }}</div>
      <button data-test="sort-current-concurrency" @click="$emit('sort', 'current_concurrency', 'asc')">
        Sort Current Concurrency
      </button>
      <div v-for="row in data" :key="row.id">
        <div
          v-if="columns.some((col) => col.key === 'id')"
          data-test="key-id"
        >
          <slot name="cell-id" :value="row.id" :row="row" />
        </div>
        <slot name="cell-name" :value="row.name" :row="row" />
        <div data-test="current-concurrency">
          <slot name="cell-current_concurrency" :value="row.current_concurrency" :row="row" />
        </div>
        <div
          v-if="columns.some((col) => col.key === 'last_used_ip')"
          data-test="last-used-ip"
        >
          <slot name="cell-last_used_ip" :value="row.last_used_ip" :row="row" />
        </div>
      </div>
      <slot name="empty" />
    </div>
  `,
}

const SelectStub = {
  name: 'Select',
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"></select>',
}

const SearchInputStub = {
  name: 'SearchInput',
  props: ['modelValue'],
  emits: ['update:modelValue', 'search'],
  template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />',
}

const PaginationStub = {
  name: 'Pagination',
  props: ['page', 'total', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  template: `
    <div>
      <button data-test="page-size-50" @click="$emit('update:pageSize', 50)">50</button>
    </div>
  `,
}

const IconStub = {
  props: ['name'],
  template: '<span data-test="icon">{{ name }}</span>',
}

const SecurityDepositDialogStub = {
  name: 'SecurityDepositDialog',
  props: ['show', 'groupId', 'resumeToken', 'resumePaymentType'],
  emits: ['close', 'success'],
  template: '<div data-test="security-deposit-dialog"></div>',
}

const mountView = async () => {
  const wrapper = mount(KeysView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: PaginationStub,
        BaseDialog: true,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        SearchInput: SearchInputStub,
        Icon: IconStub,
        UseKeyModal: true,
        EndpointPopover: true,
        GroupBadge: true,
        GroupOptionItem: true,
        SecurityDepositDialog: SecurityDepositDialogStub,
        Teleport: true,
      },
    },
  })
  await flushPromises()
  await nextTick()
  return wrapper
}

const visibleColumnKeys = (wrapper: VueWrapper) =>
  wrapper.get('[data-test="columns"]').text().split(',').filter(Boolean)

const visibleColumnMeta = (wrapper: VueWrapper): Array<{ key: string; sortable: boolean }> =>
  JSON.parse(wrapper.get('[data-test="columns-meta"]').text())

const getButtonByText = (wrapper: VueWrapper, text: string) => {
  const button = wrapper.findAll('button').find((item) => item.text().includes(text))
  if (!button) {
    throw new Error(`Button not found: ${text}`)
  }
  return button
}

function resetViewMocks(keys: ApiKey[] = [createApiKey()]) {
  localStorage.clear()
  sessionStorage.clear()
  for (const mock of [
    listKeys,
    createKey,
    updateKey,
    toggleKeyStatus,
    getPublicSettings,
    getDashboardApiKeysUsage,
    getAvailableGroups,
    getUserGroupRates,
    getSecurityDepositEligibility,
    showError,
    showSuccess,
    copyToClipboard,
    isCurrentStep,
    nextStep,
  ]) {
    mock.mockReset()
  }
  listKeys.mockResolvedValue({
    items: keys,
    total: keys.length,
    page: 1,
    page_size: 20,
    pages: keys.length > 0 ? 1 : 0,
  })
  getPublicSettings.mockResolvedValue({})
  getDashboardApiKeysUsage.mockResolvedValue({ stats: {} })
  getAvailableGroups.mockResolvedValue([])
  getUserGroupRates.mockResolvedValue({})
  getSecurityDepositEligibility.mockResolvedValue({
    data: {
      group_id: 9,
      base_required_cents: 10000,
      risk_multiplier: 1,
      required_cents: 10000,
      effective_balance_cents: 0,
      shortfall_cents: 10000,
      eligible: false,
    },
  })
  updateKey.mockImplementation(async (id: number, updates: Record<string, unknown>) => ({
    ...createApiKey(),
    id,
    ...updates,
  }))
  toggleKeyStatus.mockImplementation(async (id: number, status: ApiKey['status']) => ({
    ...createApiKey(),
    id,
    status,
  }))
  isCurrentStep.mockReturnValue(false)
}

describe('user KeysView column settings', () => {
  beforeEach(() => {
    resetViewMocks()
  })

  it('uses the default API key columns with low-frequency columns hidden', async () => {
    const wrapper = await mountView()

    expect(visibleColumnKeys(wrapper)).toEqual([
      'name',
      'key',
      'group',
      'current_concurrency',
      'usage',
      'expires_at',
      'status',
      'created_at',
      'actions',
    ])
    expect(visibleColumnKeys(wrapper)).not.toContain('rate_limit')
    expect(visibleColumnKeys(wrapper)).not.toContain('last_used_at')
    expect(visibleColumnKeys(wrapper)).not.toContain('last_used_ip')
    expect(visibleColumnKeys(wrapper)).not.toContain('id')
  })

  it('sorts API key group options by rate multiplier from low to high', async () => {
    getAvailableGroups.mockResolvedValue([
      { id: 1, name: 'High Rate', rate_multiplier: 1.5 },
      { id: 2, name: 'Low Rate', rate_multiplier: 0.5 },
      { id: 3, name: 'Standard Rate', rate_multiplier: 1 },
    ])

    const wrapper = await mountView()
    const groupOptions = (
      wrapper.vm as unknown as { groupOptions: Array<{ value: number }> }
    ).groupOptions

    expect(groupOptions.map((option) => option.value)).toEqual([2, 3, 1])
  })

  it('shows a hidden column when toggled and persists the preference', async () => {
    const wrapper = await mountView()

    await wrapper.get('button[title="Column Settings"]').trigger('click')
    await getButtonByText(wrapper, 'Rate Limit').trigger('click')
    await nextTick()

    expect(visibleColumnKeys(wrapper)).toContain('rate_limit')
    expect(localStorage.getItem('api-key-hidden-columns')).toBe(
      JSON.stringify(['id', 'last_used_at', 'last_used_ip'])
    )
    expect(localStorage.getItem('api-key-column-settings-version')).toBe('3')
  })

  it('shows the API key ID column when toggled', async () => {
    const wrapper = await mountView()

    await wrapper.get('button[title="Column Settings"]').trigger('click')
    await getButtonByText(wrapper, 'ID').trigger('click')
    await nextTick()

    expect(visibleColumnKeys(wrapper)).toContain('id')
    expect(wrapper.get('[data-test="key-id"]').text()).toBe('#1')
    expect(visibleColumnMeta(wrapper).find((column) => column.key === 'id')?.sortable).toBe(true)
  })

  it('shows the last used IP column when toggled', async () => {
    listKeys.mockResolvedValueOnce({
      items: [{ ...createApiKey(), last_used_ip: '203.0.113.10' }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    const wrapper = await mountView()

    await wrapper.get('button[title="Column Settings"]').trigger('click')
    await getButtonByText(wrapper, 'Last Used IP').trigger('click')
    await nextTick()

    expect(visibleColumnKeys(wrapper)).toContain('last_used_ip')
    expect(wrapper.get('[data-test="last-used-ip"]').text()).toBe('203.0.113.10')
  })

  it('restores column preferences from localStorage on mount', async () => {
    localStorage.setItem('api-key-hidden-columns', JSON.stringify(['group', 'created_at']))
    localStorage.setItem('api-key-column-settings-version', '1')

    const wrapper = await mountView()

    expect(visibleColumnKeys(wrapper)).toEqual([
      'name',
      'key',
      'current_concurrency',
      'usage',
      'rate_limit',
      'expires_at',
      'status',
      'last_used_at',
      'actions',
    ])
    expect(localStorage.getItem('api-key-hidden-columns')).toBe(
      JSON.stringify(['group', 'created_at', 'last_used_ip', 'id'])
    )
    expect(localStorage.getItem('api-key-column-settings-version')).toBe('3')
  })

  it('does not include always-visible columns in the toggleable menu', async () => {
    const wrapper = await mountView()

    await wrapper.get('button[title="Column Settings"]').trigger('click')
    await nextTick()

    const columnMenuText = wrapper.text()
    expect(columnMenuText).toContain('API Key')
    expect(columnMenuText).toContain('ID')
    expect(columnMenuText).toContain('Current Concurrency')
    expect(columnMenuText).toContain('Rate Limit')
    expect(columnMenuText).toContain('Last Used IP')
    expect(columnMenuText).not.toContain('Name')
    expect(columnMenuText).not.toContain('Actions')
  })

  it('renders the current concurrency value', async () => {
    const wrapper = await mountView()

    expect(wrapper.get('[data-test="current-concurrency"]').text()).toBe('3')
  })

  it('marks current concurrency as sortable', async () => {
    const wrapper = await mountView()

    const currentConcurrencyColumn = visibleColumnMeta(wrapper).find(
      (column) => column.key === 'current_concurrency'
    )
    expect(currentConcurrencyColumn?.sortable).toBe(true)
  })

  it('keeps filters and selected page size when sorting by current concurrency', async () => {
    getAvailableGroups.mockResolvedValue([{ id: 42, name: 'OpenAI' }])
    const wrapper = await mountView()

    await wrapper.get('[data-test="page-size-50"]').trigger('click')
    await flushPromises()

    await wrapper.findComponent({ name: 'SearchInput' }).vm.$emit('update:modelValue', 'target')
    await wrapper.findComponent({ name: 'SearchInput' }).vm.$emit('search')
    await flushPromises()

    const selects = wrapper.findAllComponents({ name: 'Select' })
    await selects[0].vm.$emit('update:modelValue', 42)
    await flushPromises()
    await selects[1].vm.$emit('update:modelValue', 'active')
    await flushPromises()

    listKeys.mockClear()

    await wrapper.get('[data-test="sort-current-concurrency"]').trigger('click')
    await flushPromises()

    expect(listKeys).toHaveBeenLastCalledWith(
      1,
      50,
      {
        search: 'target',
        status: 'active',
        group_id: 42,
        sort_by: 'current_concurrency',
        sort_order: 'asc',
      },
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
  })
})

describe('user KeysView create form', () => {
  beforeEach(() => resetViewMocks())

  it('does not expose custom API key creation controls', () => {
    expect(keysViewSource).not.toContain("t('keys.customKeyLabel')")
    expect(keysViewSource).not.toContain('formData.use_custom_key')
    expect(keysViewSource).not.toContain('formData.custom_key')
  })

  it('creates an insufficient key first and keeps it disabled when deposit is cancelled', async () => {
    getAvailableGroups.mockResolvedValue([
      { id: 9, name: 'Deposit Group', rate_multiplier: 1, security_deposit_base_required_cents: 10000 },
    ])
    createKey.mockResolvedValue({
      ...createApiKey(),
      group_id: 9,
      status: 'disabled',
      disabled_reason: 'security_deposit_insufficient',
    })
    const wrapper = await mountView()
    const vm = wrapper.vm as unknown as {
      formData: { name: string; group_id: number | null }
      handleSubmit: () => Promise<void>
    }
    vm.formData.name = 'deposit-key'
    vm.formData.group_id = 9

    await vm.handleSubmit()
    await flushPromises()

    expect(createKey).toHaveBeenCalled()
    expect(toggleKeyStatus).not.toHaveBeenCalled()
    const dialog = wrapper.findComponent({ name: 'SecurityDepositDialog' })
    expect(dialog.exists()).toBe(true)
    expect(dialog.props('groupId')).toBe(9)

    dialog.vm.$emit('close')
    await nextTick()

    expect(wrapper.find('[data-test="security-deposit-dialog"]').exists()).toBe(false)
    expect(toggleKeyStatus).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('security-deposit-pending-key-enable')).toBeNull()
  })

  it('continues enabling the created key after deposit succeeds', async () => {
    getAvailableGroups.mockResolvedValue([
      { id: 9, name: 'Deposit Group', rate_multiplier: 1, security_deposit_base_required_cents: 10000 },
    ])
    createKey.mockResolvedValue({
      ...createApiKey(),
      group_id: 9,
      status: 'disabled',
      disabled_reason: 'security_deposit_insufficient',
    })
    const wrapper = await mountView()
    const vm = wrapper.vm as unknown as {
      formData: { name: string; group_id: number | null }
      handleSubmit: () => Promise<void>
    }
    vm.formData.name = 'deposit-key'
    vm.formData.group_id = 9
    await vm.handleSubmit()
    await flushPromises()

    wrapper.findComponent({ name: 'SecurityDepositDialog' }).vm.$emit('success', {
      group_id: 9,
      eligible: true,
    })
    await flushPromises()

    expect(toggleKeyStatus).toHaveBeenCalledWith(1, 'active')
    expect(sessionStorage.getItem('security-deposit-pending-key-enable')).toBeNull()
  })

  it('preserves a security-locked key status when editing metadata', async () => {
    const lockedKey = { ...createApiKey(), group_id: 1, status: 'security_locked' as const }
    resetViewMocks([lockedKey])
    const wrapper = await mountView()
    const vm = wrapper.vm as unknown as {
      formData: { name: string }
      editKey: (key: ApiKey) => void
      handleSubmit: () => Promise<void>
    }
    vm.editKey(lockedKey)
    vm.formData.name = 'renamed-locked-key'

    await vm.handleSubmit()
    await flushPromises()

    expect(updateKey).toHaveBeenCalledTimes(1)
    expect(updateKey.mock.calls[0]?.[1]).not.toHaveProperty('status')
    expect(toggleKeyStatus).not.toHaveBeenCalled()
  })

  it('preserves quota-exhausted status when editing without explicit reactivation', async () => {
    const exhaustedKey = { ...createApiKey(), group_id: 1, status: 'quota_exhausted' as const }
    resetViewMocks([exhaustedKey])
    const wrapper = await mountView()
    const vm = wrapper.vm as unknown as {
      formData: { name: string }
      editKey: (key: ApiKey) => void
      handleSubmit: () => Promise<void>
    }
    vm.editKey(exhaustedKey)
    vm.formData.name = 'renamed-exhausted-key'

    await vm.handleSubmit()
    await flushPromises()

    expect(updateKey).toHaveBeenCalledTimes(1)
    expect(updateKey.mock.calls[0]?.[1]).not.toHaveProperty('status')
    expect(toggleKeyStatus).not.toHaveBeenCalled()
  })
})

describe('user KeysView security deposit group flow', () => {
  beforeEach(() => resetViewMocks([{ ...createApiKey(), group_id: 1 }]))

  it('disables in the new group before automatically enabling a no-deposit key', async () => {
    getAvailableGroups.mockResolvedValue([
      { id: 1, name: 'Old Group', rate_multiplier: 1, security_deposit_base_required_cents: 0 },
      { id: 2, name: 'New Group', rate_multiplier: 1, security_deposit_base_required_cents: 0 },
    ])
    const wrapper = await mountView()
    const vm = wrapper.vm as unknown as {
      changeGroup: (key: ApiKey, groupId: number) => Promise<void>
    }

    await vm.changeGroup({ ...createApiKey(), group_id: 1 }, 2)
    await flushPromises()

    expect(updateKey).toHaveBeenCalledWith(1, { group_id: 2, status: 'inactive' })
    expect(toggleKeyStatus).toHaveBeenCalledWith(1, 'active')
    expect(wrapper.find('[data-test="security-deposit-dialog"]').exists()).toBe(false)
  })

  it('keeps the new group disabled and opens deposit dialog when enabling is rejected', async () => {
    getAvailableGroups.mockResolvedValue([
      { id: 1, name: 'Old Group', rate_multiplier: 1, security_deposit_base_required_cents: 0 },
      { id: 9, name: 'Deposit Group', rate_multiplier: 1, security_deposit_base_required_cents: 10000 },
    ])
    toggleKeyStatus.mockRejectedValue({ code: 'SECURITY_DEPOSIT_REQUIRED' })
    const wrapper = await mountView()
    const vm = wrapper.vm as unknown as {
      changeGroup: (key: ApiKey, groupId: number) => Promise<void>
    }

    await vm.changeGroup({ ...createApiKey(), group_id: 1 }, 9)
    await flushPromises()

    expect(updateKey).toHaveBeenCalledTimes(1)
    expect(updateKey).toHaveBeenCalledWith(1, { group_id: 9, status: 'inactive' })
    expect(toggleKeyStatus).toHaveBeenCalledWith(1, 'active')
    expect(wrapper.findComponent({ name: 'SecurityDepositDialog' }).props('groupId')).toBe(9)
  })

  it('restores a pending target after returning from payment and enables only that key', async () => {
    sessionStorage.setItem('security-deposit-pending-key-enable', JSON.stringify({
      type: 'enable', keyId: 41, groupId: 9,
    }))

    await mountView()
    await flushPromises()

    expect(toggleKeyStatus).toHaveBeenCalledWith(41, 'active')
    expect(sessionStorage.getItem('security-deposit-pending-key-enable')).toBeNull()
  })
})
