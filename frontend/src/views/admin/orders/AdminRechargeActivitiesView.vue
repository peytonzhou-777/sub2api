<template>
  <AppLayout>
    <div class="space-y-4">
      <div class="flex items-center justify-end gap-2">
        <button class="btn btn-secondary" :title="t('common.refresh')" :disabled="loading" @click="loadCampaigns">
          <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
        </button>
        <button data-test="create-campaign" class="btn btn-primary inline-flex items-center gap-2" @click="openCreate">
          <Icon name="plus" size="sm" />
          {{ t('payment.rechargeActivities.create') }}
        </button>
      </div>

      <div class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-600 dark:bg-dark-800">
        <div v-if="loading" class="py-16 text-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="campaigns.length === 0" data-test="campaign-empty" class="py-16 text-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('payment.rechargeActivities.empty') }}
        </div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-600">
            <thead class="bg-gray-50 dark:bg-dark-700">
              <tr>
                <th class="table-header">{{ t('payment.rechargeActivities.name') }}</th>
                <th class="table-header">{{ t('payment.rechargeActivities.period') }}</th>
                <th class="table-header">{{ t('payment.rechargeActivities.rules') }}</th>
                <th class="table-header">{{ t('common.status') }}</th>
                <th class="table-header">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="campaign in campaigns" :key="campaign.id" class="align-top">
                <td class="table-cell min-w-64">
                  <p data-test="campaign-name" class="font-medium text-gray-900 dark:text-white">{{ campaign.name }}</p>
                  <p
                    v-if="campaign.description"
                    data-test="campaign-description"
                    class="mt-1 max-w-sm whitespace-pre-line text-xs leading-5 text-gray-500 line-clamp-2 dark:text-gray-400"
                    :title="campaign.description"
                  >
                    {{ campaign.description }}
                  </p>
                </td>
                <td class="table-cell whitespace-nowrap">
                  <p>{{ formatBrowserDateTime(campaign.start_at) }}</p>
                  <p class="mt-1 text-gray-500 dark:text-gray-400">{{ formatBrowserDateTime(campaign.end_at) }}</p>
                </td>
                <td class="table-cell min-w-52">
                  <p>{{ t('payment.rechargeActivities.tierCount', { count: campaign.tiers.length }) }}</p>
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                    {{ campaign.participation_limit === 0
                      ? t('payment.rechargeActivities.unlimited')
                      : t('payment.rechargeActivities.limitCount', { count: campaign.participation_limit }) }}
                  </p>
                </td>
                <td class="table-cell">
                  <span :class="statusClass(campaign.status)" class="badge">
                    {{ t('payment.rechargeActivities.status.' + campaign.status) }}
                  </span>
                </td>
                <td class="table-cell">
                  <div class="flex items-center gap-1">
                    <button
                      v-if="campaign.status === 'scheduled'"
                      data-test="edit-campaign"
                      class="icon-action hover:bg-blue-50 hover:text-blue-600 dark:hover:bg-blue-900/20 dark:hover:text-blue-400"
                      :title="t('common.edit')"
                      @click="openEdit(campaign)"
                    >
                      <Icon name="edit" size="sm" />
                    </button>
                    <button
                      v-if="campaign.status === 'scheduled'"
                      data-test="delete-campaign"
                      class="icon-action hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                      :title="t('common.delete')"
                      @click="deleteCampaign(campaign)"
                    >
                      <Icon name="trash" size="sm" />
                    </button>
                    <button
                      v-if="campaign.status === 'active'"
                      data-test="end-campaign"
                      class="icon-action hover:bg-amber-50 hover:text-amber-600 dark:hover:bg-amber-900/20 dark:hover:text-amber-400"
                      :title="t('payment.rechargeActivities.endEarly')"
                      @click="endCampaign(campaign)"
                    >
                      <Icon name="ban" size="sm" />
                    </button>
                    <span v-if="campaign.status === 'ended'" class="text-xs text-gray-400">-</span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div v-if="showForm" class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" @click.self="closeForm">
      <form class="max-h-[92vh] w-full max-w-5xl overflow-y-auto rounded-lg bg-white shadow-xl dark:bg-dark-800" @submit.prevent="saveCampaign">
        <div class="flex items-center justify-between border-b border-gray-200 px-6 py-4 dark:border-dark-600">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">
            {{ editingId ? t('payment.rechargeActivities.edit') : t('payment.rechargeActivities.create') }}
          </h2>
          <button type="button" class="icon-action" :title="t('common.close')" @click="closeForm">
            <Icon name="x" size="md" />
          </button>
        </div>

        <div class="space-y-5 p-6">
          <div class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1.5">
              <span class="form-label">{{ t('payment.rechargeActivities.name') }}</span>
              <input
                v-model="form.name"
                data-test="campaign-name-input"
                class="input"
                type="text"
                maxlength="100"
                required
              />
            </label>
            <label class="space-y-1.5">
              <span class="form-label">{{ t('payment.rechargeActivities.participationLimit') }}</span>
              <input
                v-model.number="form.participation_limit"
                data-test="campaign-limit-input"
                class="input"
                type="number"
                min="0"
                step="1"
                required
              />
            </label>
          </div>

          <label class="block space-y-1.5">
            <span class="form-label">{{ t('payment.rechargeActivities.description') }}</span>
            <textarea
              v-model="form.description"
              data-test="campaign-description-input"
              class="input min-h-24 resize-y"
              maxlength="1000"
              rows="4"
            />
          </label>

          <div class="grid gap-4 md:grid-cols-2">
            <label class="space-y-1.5">
              <span class="form-label">{{ t('payment.rechargeActivities.startAt') }}</span>
              <input
                v-model="form.start_at"
                data-test="campaign-start-input"
                class="input"
                type="datetime-local"
                step="0.001"
                required
              />
            </label>
            <label class="space-y-1.5">
              <span class="form-label">{{ t('payment.rechargeActivities.endAt') }}</span>
              <input
                v-model="form.end_at"
                data-test="campaign-end-input"
                class="input"
                type="datetime-local"
                step="0.001"
                required
              />
            </label>
          </div>

          <section class="space-y-3">
            <div class="flex items-center justify-between">
              <h3 class="form-label">{{ t('payment.rechargeActivities.tiers') }}</h3>
              <button data-test="add-tier" type="button" class="btn btn-secondary inline-flex items-center gap-1.5" @click="addTier">
                <Icon name="plus" size="sm" />
                {{ t('payment.rechargeActivities.addTier') }}
              </button>
            </div>

            <div class="space-y-2">
              <div
                v-for="(tier, index) in form.tiers"
                :key="index"
                data-test="tier-row"
                class="grid items-end gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600 md:grid-cols-[repeat(4,minmax(0,1fr))_2.5rem]"
              >
                <label class="space-y-1.5">
                  <span class="form-label">{{ t('payment.rechargeActivities.minAmount') }}</span>
                  <input v-model.number="tier.min_amount" :data-test="'tier-min-amount-' + index" class="input" type="number" min="0" step="0.00000001" required />
                </label>
                <label class="space-y-1.5">
                  <span class="form-label">{{ t('payment.rechargeActivities.maxAmount') }}</span>
                  <input v-model.number="tier.max_amount" :data-test="'tier-max-amount-' + index" class="input" type="number" min="0" step="0.00000001" required />
                </label>
                <label class="space-y-1.5">
                  <span class="form-label">{{ t('payment.rechargeActivities.minRate') }}</span>
                  <input v-model.number="tier.min_rate" :data-test="'tier-min-rate-' + index" class="input" type="number" min="0" step="0.00000001" required />
                </label>
                <label class="space-y-1.5">
                  <span class="form-label">{{ t('payment.rechargeActivities.maxRate') }}</span>
                  <input v-model.number="tier.max_rate" :data-test="'tier-max-rate-' + index" class="input" type="number" min="0" step="0.00000001" required />
                </label>
                <button
                  type="button"
                  :data-test="'delete-tier-' + index"
                  class="icon-action mb-1 text-red-500 disabled:cursor-not-allowed disabled:opacity-30"
                  :title="t('common.delete')"
                  :disabled="form.tiers.length === 1"
                  @click="removeTier(index)"
                >
                  <Icon name="trash" size="sm" />
                </button>
              </div>
            </div>
          </section>

          <p v-if="formError" class="text-sm text-red-600 dark:text-red-400">{{ formError }}</p>
        </div>

        <div class="flex justify-end gap-2 border-t border-gray-200 px-6 py-4 dark:border-dark-600">
          <button type="button" class="btn btn-secondary" @click="closeForm">{{ t('common.cancel') }}</button>
          <button data-test="save-campaign" type="submit" class="btn btn-primary" :disabled="saving">
            {{ saving ? t('common.processing') : t('common.save') }}
          </button>
        </div>
      </form>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminPaymentAPI } from '@/api/admin/payment'
import { useAppStore } from '@/stores/app'
import type { RechargeBonusCampaign, RechargeBonusCampaignInput, RechargeBonusTier } from '@/types/payment'
import { formatBrowserDateTime, localDateTimeInputToUTC, utcToLocalDateTimeInput } from '@/utils/browserDateTime'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'

interface CampaignForm {
  name: string
  description: string
  start_at: string
  end_at: string
  participation_limit: number
  tiers: RechargeBonusTier[]
}

const { t } = useI18n()
const appStore = useAppStore()
const campaigns = ref<RechargeBonusCampaign[]>([])
const loading = ref(false)
const originalStartAtUTC = ref<string | null>(null)
const originalEndAtUTC = ref<string | null>(null)
const saving = ref(false)
const showForm = ref(false)
const editingId = ref<number | null>(null)
const formError = ref('')
const form = ref<CampaignForm>(newCampaignForm())

function localInputFor(date: Date): string {
  return utcToLocalDateTimeInput(date.toISOString())
}

function newCampaignForm(): CampaignForm {
  const start = new Date()
  start.setSeconds(0, 0)
  start.setMinutes(start.getMinutes() + 1)
  const end = new Date(start)
  end.setDate(end.getDate() + 7)
  return {
    name: '',
    description: '',
    start_at: localInputFor(start),
    end_at: localInputFor(end),
    participation_limit: 0,
    tiers: [{ min_amount: 10, max_amount: 100, min_rate: 5, max_rate: 5 }],
  }
}

function statusClass(status: RechargeBonusCampaign['status']): string {
  if (status === 'active') return 'badge-success'
  if (status === 'scheduled') return 'badge-info'
  return 'badge-neutral'
}

/** 加载全部充值赠送活动，并按开始时间倒序展示。 */
async function loadCampaigns() {
  loading.value = true
  try {
    const response = await adminPaymentAPI.getRechargeBonusCampaigns()
    campaigns.value = [...(response.data || [])].sort((a, b) => b.start_at.localeCompare(a.start_at))
  } catch {
    appStore.showError(t('payment.rechargeActivities.loadFailed'))
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingId.value = null
  originalStartAtUTC.value = null
  originalEndAtUTC.value = null
  form.value = newCampaignForm()
  formError.value = ''
  showForm.value = true
}

function openEdit(campaign: RechargeBonusCampaign) {
  editingId.value = campaign.id
  originalStartAtUTC.value = campaign.start_at
  originalEndAtUTC.value = campaign.end_at
  form.value = {
    name: campaign.name,
    description: campaign.description,
    start_at: utcToLocalDateTimeInput(campaign.start_at),
    end_at: utcToLocalDateTimeInput(campaign.end_at),
    participation_limit: campaign.participation_limit,
    tiers: campaign.tiers.map(tier => ({ ...tier })),
  }
  formError.value = ''
  showForm.value = true
}

function closeForm() {
  if (saving.value) return
  showForm.value = false
}

function addTier() {
  const previous = form.value.tiers[form.value.tiers.length - 1]
  const min = previous?.max_amount ?? 0
  const rate = previous?.max_rate ?? 0
  form.value.tiers.push({ min_amount: min, max_amount: min + 100, min_rate: rate, max_rate: rate })
}

function removeTier(index: number) {
  if (form.value.tiers.length > 1) form.value.tiers.splice(index, 1)
}

function isFiniteNonNegative(value: number): boolean {
  return Number.isFinite(value) && value >= 0
}

function validateForm(): RechargeBonusCampaignInput | null {
  const name = form.value.name.trim()
  if (!name || name.length > 100 || form.value.description.length > 1000) return null
  if (!Number.isInteger(form.value.participation_limit) || form.value.participation_limit < 0) return null

  let startAt: string
  let endAt: string
  try {
    startAt = localDateTimeInputToUTC(form.value.start_at, originalStartAtUTC.value)
    endAt = localDateTimeInputToUTC(form.value.end_at, originalEndAtUTC.value)
  } catch {
    return null
  }
  if (Date.parse(endAt) <= Date.parse(startAt)) return null

  const tiers = form.value.tiers.map(tier => ({ ...tier })).sort((a, b) => a.min_amount - b.min_amount)
  if (tiers.length === 0) return null
  for (let index = 0; index < tiers.length; index += 1) {
    const tier = tiers[index]
    if (!isFiniteNonNegative(tier.min_amount) || !isFiniteNonNegative(tier.max_amount)
      || !isFiniteNonNegative(tier.min_rate) || !isFiniteNonNegative(tier.max_rate)
      || tier.max_amount <= tier.min_amount) return null
    if (index > 0 && tier.min_amount < tiers[index - 1].max_amount) return null
  }

  return {
    name,
    description: form.value.description,
    start_at: startAt,
    end_at: endAt,
    participation_limit: form.value.participation_limit,
    tiers,
  }
}

/** 保存预约活动；接口时间始终使用 UTC。 */
async function saveCampaign() {
  const payload = validateForm()
  if (!payload) {
    formError.value = t('payment.rechargeActivities.invalidForm')
    return
  }

  saving.value = true
  formError.value = ''
  try {
    if (editingId.value) {
      await adminPaymentAPI.updateRechargeBonusCampaign(editingId.value, payload)
    } else {
      await adminPaymentAPI.createRechargeBonusCampaign(payload)
    }
    showForm.value = false
    appStore.showSuccess(t('common.saved'))
    await loadCampaigns()
  } catch {
    formError.value = t('payment.rechargeActivities.saveFailed')
  } finally {
    saving.value = false
  }
}

async function deleteCampaign(campaign: RechargeBonusCampaign) {
  if (!window.confirm(t('payment.rechargeActivities.deleteConfirm'))) return
  try {
    await adminPaymentAPI.deleteRechargeBonusCampaign(campaign.id)
    appStore.showSuccess(t('common.deleted'))
    await loadCampaigns()
  } catch {
    appStore.showError(t('payment.rechargeActivities.deleteFailed'))
  }
}

/** 将生效中的活动结束时间缩短为当前 UTC 时间。 */
async function endCampaign(campaign: RechargeBonusCampaign) {
  if (!window.confirm(t('payment.rechargeActivities.endConfirm'))) return
  try {
    await adminPaymentAPI.updateRechargeBonusCampaign(campaign.id, {
      name: campaign.name,
      description: campaign.description,
      start_at: campaign.start_at,
      end_at: new Date().toISOString(),
      participation_limit: campaign.participation_limit,
      tiers: campaign.tiers,
    })
    appStore.showSuccess(t('payment.rechargeActivities.ended'))
    await loadCampaigns()
  } catch {
    appStore.showError(t('payment.rechargeActivities.saveFailed'))
  }
}

onMounted(loadCampaigns)
</script>

<style scoped>
.table-header {
  @apply px-4 py-3 text-left text-xs font-semibold text-gray-500 dark:text-gray-300;
}

.table-cell {
  @apply px-4 py-3 text-sm text-gray-700 dark:text-gray-300;
}

.form-label {
  @apply block text-xs font-medium text-gray-600 dark:text-gray-300;
}

.icon-action {
  @apply inline-flex h-9 w-9 items-center justify-center rounded-md text-gray-500 transition-colors;
}

.badge-info {
  @apply bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300;
}

.badge-neutral {
  @apply bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-300;
}
</style>
