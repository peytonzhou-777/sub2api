import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import Pagination from '../Pagination.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, number>) =>
      key === 'pagination.goToPage' ? `第 ${params?.page} 页` : key
  })
}))

describe('Pagination', () => {
  it('uses the Codex blue semantic class for the current page', () => {
    const wrapper = mount(Pagination, {
      props: {
        total: 40,
        page: 1,
        pageSize: 20,
        showPageSizeSelector: false
      }
    })

    const currentPage = wrapper.get('[aria-current="page"]')
    expect(currentPage.classes()).toContain('pagination-page-active')
    expect(currentPage.classes()).not.toContain('bg-primary-50')
  })
})
