<template>
  <div class="empty-state">
    <!-- Title -->
    <h3 class="empty-state-title">
      {{ displayTitle }}
    </h3>

    <!-- Description -->
    <p class="empty-state-description">
      {{ description }}
    </p>

    <!-- Action -->
    <div v-if="actionText || $slots.action" class="mt-6">
      <slot name="action">
        <component
          :is="actionTo ? 'RouterLink' : 'button'"
          v-if="actionText"
          :to="actionTo"
          @click="!actionTo && $emit('action')"
          class="btn btn-primary"
        >
          <Icon v-if="actionIcon" name="plus" size="md" />
          {{ actionText }}
        </component>
      </slot>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()

interface Props {
  title?: string
  description?: string
  actionText?: string
  actionTo?: string | object
  actionIcon?: boolean
  message?: string
}

const props = withDefaults(defineProps<Props>(), {
  description: '',
  actionIcon: true
})

const displayTitle = computed(() => props.title || t('common.noData'))

defineEmits(['action'])
</script>
