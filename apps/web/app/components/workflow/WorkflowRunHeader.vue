<script setup lang="ts">
import { Badge } from '@/components/ui/badge'
import ActorBadge from '@/components/ActorBadge.vue'
import type { WorkflowRun, WorkflowRunStatus } from '~/types/workflow'
import { relativeTime } from '~/utils/time'

defineProps<{
  run: WorkflowRun
}>()

const { t } = useI18n()

function runStatusVariant(s: WorkflowRunStatus) {
  switch (s) {
    case 'success': return 'default' as const
    case 'failed': return 'destructive' as const
    case 'running': return 'default' as const
    case 'cancelled': return 'outline' as const
    default: return 'secondary' as const
  }
}

function shortSha(s: string) { return s.slice(0, 7) }

function rel(s?: string | null) { return relativeTime(s ?? null, t) }

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
</script>

<template>
  <header class="space-y-3">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="space-y-1 min-w-0">
        <h1 class="text-2xl font-semibold tracking-tight truncate">
          {{ run.workflow_name }}
        </h1>
        <p class="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
          <Badge :variant="runStatusVariant(run.status)">
            {{ t(`repo.workflows.status.${run.status}`) }}
          </Badge>
          <span>{{ t(`repo.workflows.event.${run.event_name}`) }}</span>
          <span>·</span>
          <code class="font-mono text-xs">{{ run.ref }}</code>
          <span>·</span>
          <code class="font-mono text-xs">{{ shortSha(run.commit_sha) }}</code>
          <span>·</span>
          <span>{{ rel(run.created_at) }}</span>
          <template v-if="run.trigger_actor">
            <span>·</span>
            <span class="inline-flex items-center gap-1">
              {{ t('repo.workflows.triggeredBy') }}:
              <ActorBadge :actor="run.trigger_actor" size="sm" />
            </span>
          </template>
          <template v-if="run.started_at">
            <span>·</span>
            <span>{{ duration(run.started_at, run.finished_at) }}</span>
          </template>
        </p>
      </div>
    </div>
  </header>
</template>
