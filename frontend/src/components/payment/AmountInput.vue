<template>
  <div class="space-y-2">
    <label class="block text-sm font-medium text-gray-700 dark:text-gray-300">
      {{ t('payment.customAmount') }}
    </label>
    <input
      data-test="amount-slider"
      class="amount-slider"
      type="range"
      min="0"
      :max="sliderStops.length - 1"
      step="1"
      :value="sliderIndex"
      :aria-label="t('payment.customAmount')"
      @input="handleSliderInput"
    />
    <div class="flex items-center justify-center gap-2 overflow-x-auto pb-1">
      <button
        v-for="delta in [-10, -5, -1]"
        :key="delta"
        :data-test="`amount-adjust-${delta}`"
        type="button"
        class="amount-adjustment-button"
        :disabled="!canAdjust(delta)"
        @click="adjustAmount(delta)"
      >
        {{ delta }}
      </button>
      <div class="relative">
        <span class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-dark-500">
          $
        </span>
        <input
          data-test="amount-number-input"
          type="number"
          inputmode="decimal"
          step="any"
          :min="sliderMin"
          :max="sliderMax"
          :value="customText"
          :placeholder="placeholderText"
          class="input w-28 py-2.5 pl-8 pr-3 text-center"
          @input="handleNumberInput"
          @blur="normalizeInput"
        />
      </div>
      <button
        v-for="delta in [1, 5, 10]"
        :key="delta"
        :data-test="`amount-adjust-${delta}`"
        type="button"
        class="amount-adjustment-button"
        :disabled="!canAdjust(delta)"
        @click="adjustAmount(delta)"
      >
        +{{ delta }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  modelValue: number | null
  min?: number
  max?: number
}>(), {
  min: 0,
  max: 0,
})

const emit = defineEmits<{
  'update:modelValue': [value: number | null]
}>()

const { t } = useI18n()
const customText = ref('')

const sliderMin = computed(() => Math.max(1, Math.ceil(props.min || 1)))
const sliderMax = computed(() => {
  const upperBound = props.max > 0
    ? Math.floor(props.max)
    : Math.max(5000, Math.ceil(props.modelValue ?? 0))
  return Math.max(sliderMin.value, upperBound)
})

// 将三段梯度金额映射为等距滑杆刻度。
const sliderStops = computed(() => {
  const stops: number[] = []
  const addStop = (value: number) => {
    if (value < sliderMin.value || value > sliderMax.value) return
    if (stops.at(-1) !== value) stops.push(value)
  }

  addStop(sliderMin.value)
  for (let value = sliderMin.value; value <= Math.min(sliderMax.value, 100); value += 1) addStop(value)
  for (let value = Math.max(105, Math.ceil(sliderMin.value / 5) * 5); value <= Math.min(sliderMax.value, 500); value += 5) addStop(value)
  for (let value = Math.max(510, Math.ceil(sliderMin.value / 10) * 10); value <= sliderMax.value; value += 10) addStop(value)
  addStop(sliderMax.value)
  return stops
})

const sliderIndex = computed(() => {
  const target = props.modelValue ?? sliderMin.value
  return sliderStops.value.reduce((bestIndex, stop, index) =>
    Math.abs(stop - target) < Math.abs(sliderStops.value[bestIndex] - target) ? index : bestIndex, 0)
})

const placeholderText = computed(() => `${sliderMin.value} - ${sliderMax.value}`)

function clampAmount(value: number): number {
  return Math.min(Math.max(value, sliderMin.value), sliderMax.value)
}

function updateAmount(value: number) {
  const next = clampAmount(value)
  customText.value = String(next)
  emit('update:modelValue', next)
}

function handleSliderInput(event: Event) {
  const index = Number((event.target as HTMLInputElement).value)
  updateAmount(sliderStops.value[index] ?? sliderMin.value)
}

function handleNumberInput(event: Event) {
  const value = (event.target as HTMLInputElement).value
  customText.value = value
  if (value === '') {
    emit('update:modelValue', null)
    return
  }
  const parsed = Number(value)
  if (Number.isFinite(parsed)) emit('update:modelValue', parsed)
}

function normalizeInput() {
  if (customText.value === '') {
    updateAmount(sliderMin.value)
    return
  }
  updateAmount(Number(customText.value))
}

function currentAmount(): number {
  const typed = Number(customText.value)
  if (customText.value !== '' && Number.isFinite(typed)) return typed
  return props.modelValue ?? sliderMin.value
}

function canAdjust(delta: number): boolean {
  const next = currentAmount() + delta
  return next >= sliderMin.value && (props.max <= 0 || next <= props.max)
}

function adjustAmount(delta: number) {
  if (!canAdjust(delta)) return
  updateAmount(currentAmount() + delta)
}

watch(() => props.modelValue, (value) => {
  if (value === null) {
    customText.value = ''
    return
  }
  if (String(value) !== customText.value) customText.value = String(value)
}, { immediate: true })
</script>

<style scoped>
.amount-slider {
  width: 100%;
  height: 20px;
  cursor: ew-resize;
  appearance: none;
  background: transparent;
}

.amount-slider::-webkit-slider-runnable-track {
  height: 6px;
  border-radius: 999px;
  background: #444;
}

.amount-slider::-webkit-slider-thumb {
  width: 18px;
  height: 18px;
  margin-top: -6px;
  appearance: none;
  border: 2px solid #f4f4f4;
  border-radius: 50%;
  background: #2f9cff;
  box-shadow: 0 1px 5px rgb(0 0 0 / 0.4);
}

.amount-slider::-moz-range-track {
  height: 6px;
  border-radius: 999px;
  background: #444;
}

.amount-slider::-moz-range-thumb {
  width: 14px;
  height: 14px;
  border: 2px solid #f4f4f4;
  border-radius: 50%;
  background: #2f9cff;
  box-shadow: 0 1px 5px rgb(0 0 0 / 0.4);
}

.amount-adjustment-button {
  min-width: 40px;
  border: 1px solid #343434;
  border-radius: 8px;
  padding: 8px 9px;
  background: #202020;
  color: #d4d4d4;
  font-size: 12px;
  font-weight: 500;
  transition: border-color 150ms ease, background-color 150ms ease, color 150ms ease, opacity 150ms ease;
}

.amount-adjustment-button:hover:not(:disabled) {
  border-color: #505050;
  background: #2a2a2a;
  color: #fff;
}

.amount-adjustment-button:disabled {
  cursor: not-allowed;
  opacity: 0.32;
}
</style>
