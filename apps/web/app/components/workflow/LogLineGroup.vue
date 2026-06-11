<script setup lang="ts">
import type { WorkflowJobLogLine } from '~/types/workflow'

defineProps<{
  lines: WorkflowJobLogLine[]
}>()

function streamClass(s: string) {
  switch (s) {
    case 'stderr': return 'text-red-400'
    case 'system': return 'text-muted-foreground/60'
    default: return ''
  }
}
</script>

<template>
  <div class="font-mono text-xs leading-relaxed">
    <div
      v-for="line in lines"
      :key="line.id"
      :class="streamClass(line.stream)"
    >
      <span
        v-if="line.stream !== 'stdout'"
        class="text-[10px] text-muted-foreground mr-1 select-none"
      >
        [{{ line.stream }}]
      </span>
      <span class="whitespace-pre-wrap break-all">{{ line.line }}</span>
    </div>
  </div>
</template>
