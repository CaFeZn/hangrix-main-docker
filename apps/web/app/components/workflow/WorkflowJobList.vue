<script setup lang="ts">
import { Badge } from '@/components/ui/badge'
import type { WorkflowJobRun, WorkflowJobStatus } from '~/types/workflow'

const props = defineProps<{
  jobs: WorkflowJobRun[]
  selectedJobId: number | null
}>()

const emit = defineEmits<{
  select: [jobId: number]
}>()

const { t } = useI18n()

function jobStatusVariant(s: WorkflowJobStatus) {
  switch (s) {
    case 'success': return 'default' as const
    case 'failed': return 'destructive' as const
    case 'running': return 'default' as const
    case 'cancelled': return 'outline' as const
    case 'skipped': return 'secondary' as const
    default: return 'secondary' as const
  }
}

function duration(started: string | null, finished: string | null): string {
  if (!started) return '—'
  const start = Date.parse(started)
  if (Number.isNaN(start)) return '—'
  const end = finished ? Date.parse(finished) : Date.now()
  if (Number.isNaN(end)) return '—'
  const diffSec = Math.max(0, Math.round((end - start) / 1000))
  if (diffSec < 60) return `${diffSec}s`
  const min = Math.floor(diffSec / 60)
  const sec = diffSec % 60
  if (min < 60) return `${min}m ${sec}s`
  const hr = Math.floor(min / 60)
  return `${hr}h ${Math.floor(min % 60)}m`
}

// Mobile: selected job id from the <select>
const mobileSelected = defineModel<number | null>('mobileSelected', { default: null })
</script>

<template>
  <!-- Desktop: vertical job list -->
  <div class="hidden md:block">
    <p class="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3 px-1">
      {{ t('repo.workflows.job.title') }}
      <span class="text-muted-foreground/50 ml-1">({{ jobs.length }})</span>
    </p>
    <p v-if="jobs.length === 0" class="text-sm text-muted-foreground px-1">
      {{ t('repo.workflows.job.empty') }}
    </p>
    <div v-else class="space-y-0.5">
      <button
        v-for="job in jobs"
        :key="job.id"
        type="button"
        class="w-full text-left px-3 py-2.5 rounded-md transition-colors border"
        :class="selectedJobId === job.id
          ? 'bg-accent border-accent-foreground/20'
          : 'border-transparent hover:bg-muted/50'"
        @click="emit('select', job.id)"
      >
        <div class="flex items-center gap-2 min-w-0">
          <span class="text-sm font-medium truncate flex-1">
            {{ job.display_name || job.job_key }}
          </span>
          <Badge :variant="jobStatusVariant(job.status)" class="text-xs shrink-0">
            {{ t(`repo.workflows.status.${job.status}`) }}
          </Badge>
        </div>
        <p class="text-xs text-muted-foreground mt-0.5">
          {{ duration(job.started_at, job.finished_at) }}
          <span v-if="job.runner_id" class="mx-1">·</span>
          <span v-if="job.runner_id">{{ t('repo.workflows.job.runnerIdFmt', { id: job.runner_id }) }}</span>
          <span v-if="job.exit_code !== null" class="mx-1">·</span>
          <span v-if="job.exit_code !== null">{{ t('repo.workflows.job.exitCodeFmt', { code: job.exit_code }) }}</span>
        </p>
      </button>
    </div>
  </div>

  <!-- Mobile: dropdown select -->
  <div class="md:hidden">
    <label class="text-xs font-semibold text-muted-foreground uppercase tracking-wider block mb-1.5">
      {{ t('repo.workflows.job.title') }}
    </label>
    <select
      v-if="jobs.length > 0"
      class="w-full rounded-md border bg-background px-3 py-2 text-sm"
      :value="selectedJobId ?? ''"
      @change="emit('select', Number(($event.target as HTMLSelectElement).value))"
    >
      <option v-for="job in jobs" :key="job.id" :value="job.id">
        {{ job.display_name || job.job_key }} — {{ t(`repo.workflows.status.${job.status}`) }}
      </option>
    </select>
    <p v-else class="text-sm text-muted-foreground">
      {{ t('repo.workflows.job.empty') }}
    </p>
  </div>
</template>
