<script setup lang="ts">
import { computed } from 'vue'
import { AlertTriangle, Clock, VolumeX } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import type { RepoSilence } from '~/types/silence'
import { deriveSilenceBanner } from '~/composables/useSilence'

const props = withDefaults(defineProps<{
  state: RepoSilence | null
  loading?: boolean
  /** Set to true on issue pages where the banner occupies the sticky-header area. */
  compact?: boolean
  /** When true, renders a "Resume" button (only for manage-level users). */
  canManage?: boolean
}>(), {
  loading: false,
  compact: false,
  canManage: false,
})

const { t } = useI18n()

const banner = computed(() => deriveSilenceBanner(props.state))

const label = computed(() => {
  if (!banner.value) return ''
  const b = banner.value
  if (b.source === 'manual') {
    return t('repo.silence.bannerManual', { by: b.sourceRef })
  }
  if (b.source === 'schedule') {
    return t('repo.silence.bannerSchedule', { name: b.sourceRef })
  }
  return t('repo.silence.bannerSilenced')
})

const exitTimeLabel = computed(() => {
  if (!banner.value?.exitTime) return ''
  return banner.value.exitTime.toLocaleTimeString()
})

const variantClasses = computed(() => {
  if (!banner.value) return ''
  if (banner.value.variant === 'destructive') {
    return 'border-destructive/40 bg-destructive/5 text-destructive'
  }
  return 'border-amber-500/40 bg-amber-50 text-amber-800 dark:bg-amber-950/20 dark:text-amber-400'
})
</script>

<template>
  <div
    v-if="banner"
    :class="[
      'flex flex-wrap items-center gap-2 rounded-md border px-3',
      compact ? 'py-1.5' : 'py-2.5',
      variantClasses,
    ]"
  >
    <VolumeX class="size-4 shrink-0" />
    <span class="text-sm font-medium">{{ label }}</span>
    <span
      v-if="exitTimeLabel"
      class="flex items-center gap-1 text-xs opacity-80"
    >
      <Clock class="size-3" />
      {{ t('repo.silence.expectedResume', { time: exitTimeLabel }) }}
    </span>
    <span v-if="banner.reason" class="text-xs opacity-70">
      — {{ banner.reason }}
    </span>
    <Button
      v-if="canManage"
      variant="outline"
      size="sm"
      class="ml-auto h-7 shrink-0"
    >
      {{ t('repo.silence.exitSilence') }}
    </Button>
  </div>
</template>
