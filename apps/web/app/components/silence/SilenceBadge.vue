<script setup lang="ts">
import { computed } from 'vue'
import { VolumeX, ShieldCheck } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/utils/utils'

const props = withDefaults(defineProps<{
  silenced?: boolean
  overridden?: boolean
  class?: string
}>(), {
  silenced: false,
  overridden: false,
})

const { t } = useI18n()

const variant = computed(() => {
  if (props.overridden) return 'secondary'
  if (props.silenced) return 'destructive'
  return undefined
})

const label = computed(() => {
  if (props.overridden) return t('repo.silence.overrideActive')
  if (props.silenced) return t('repo.silence.silenced')
  return ''
})

const icon = computed(() => {
  if (props.overridden) return ShieldCheck
  return VolumeX
})
</script>

<template>
  <Badge
    v-if="silenced || overridden"
    :variant="variant"
    :class="cn('gap-1', props.class)"
  >
    <component :is="icon" class="size-3" />
    {{ label }}
  </Badge>
</template>
