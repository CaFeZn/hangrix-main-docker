<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import LogLineGroup from './LogLineGroup.vue'
import type { WorkflowJobRun, WorkflowJobLogLine, WorkflowJobStatus, WorkflowOutputValue } from '~/types/workflow'
import type { StepLogGroup } from '~/composables/useWorkflowLogs'

const props = defineProps<{
  job: WorkflowJobRun | null
  stepGroups: StepLogGroup[]
  lines: WorkflowJobLogLine[]
  loading: boolean
  error: string | null
  autoScroll: boolean
}>()

const emit = defineEmits<{
  'update:autoScroll': [value: boolean]
}>()

const { t } = useI18n()

// Track expanded state per step. Key is stepId.
const expanded = ref<Record<string, boolean>>({})
const logsContainer = ref<HTMLElement | null>(null)

// Auto-expand rules:
// - The currently running step (when job is running): expand the step that has
//   the most recent log line
// - The last failed step: expand the last step in the list when job failed
// We detect this on mount and when stepGroups change.

function applyAutoExpand() {
  if (!props.job) return

  const newExpanded: Record<string, boolean> = {}
  // Persist existing manual toggles
  for (const key of Object.keys(expanded.value)) {
    const v = expanded.value[key]
    if (v !== undefined) newExpanded[key] = v
  }

  if (props.job.status === 'running') {
    // Expand the step that has the most recent log line (currently running step)
    if (props.lines.length > 0) {
      const lastLine = props.lines[props.lines.length - 1]
      if (lastLine) {
        const runningStepId = lastLine.step_id || '_uncategorized'
        newExpanded[runningStepId] = true
      }
    }
  } else if (props.job.status === 'failed') {
    // Expand the last step in the list (by definition order)
    if (props.stepGroups.length > 0) {
      const lastGroup = props.stepGroups[props.stepGroups.length - 1]
      if (lastGroup) {
        newExpanded[lastGroup.stepId] = true
      }
    }
  }

  expanded.value = newExpanded
}

// Apply auto-expand when stepGroups change
watch(() => props.stepGroups, applyAutoExpand, { immediate: true })
watch(() => props.job?.status, applyAutoExpand)

function toggleStep(stepId: string) {
  expanded.value = {
    ...expanded.value,
    [stepId]: !(expanded.value[stepId] ?? false),
  }
}

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

function scrollToBottom() {
  if (!props.autoScroll || !logsContainer.value) return
  logsContainer.value.scrollTop = logsContainer.value.scrollHeight
}

// Scroll on new lines
watch(() => props.lines.length, () => {
  setTimeout(scrollToBottom, 50)
})

// Build step display groups.
//
// When job.steps metadata is available we drive the list from metadata so
// every defined step appears — even steps that have not yet produced any
// log output.  Log lines are then merged into their corresponding steps.
// Uncategorised log lines (step_id = null) and groups that don't match any
// defined step are appended at the end.
//
// Without metadata we fall back to the log-line-driven grouping.
const resolvedStepGroups = computed(() => {
  const stepById = new Map<string, string>()
  const stepByIndex = new Map<string, string>()
  if (props.job?.steps) {
    for (let i = 0; i < props.job.steps.length; i++) {
      const step = props.job.steps[i]!
      if (step.id) stepById.set(step.id, step.name)
      if (step.name) stepById.set(step.name, step.name)
      stepByIndex.set(String(i + 1), step.name)
    }
  }

  // Drive from job.steps metadata when available.
  if (props.job?.steps && props.job.steps.length > 0) {
    const coveredKeys = new Set<string>()
    const result: StepLogGroup[] = props.job.steps.map((step, i) => {
      const stepKey = step.id || String(i + 1)
      coveredKeys.add(stepKey)
      if (step.name) coveredKeys.add(step.name)
      const logGroup = props.stepGroups.find(
        (g) => g.stepId === stepKey || g.stepId === step.name,
      )
      return {
        stepId: stepKey,
        stepName: step.name,
        lines: logGroup?.lines ?? [],
      }
    })

    // Append any leftover groups from logs not covered by steps
    // (e.g. _uncategorized lines with step_id = null).
    for (const g of props.stepGroups) {
      if (!coveredKeys.has(g.stepId)) {
        result.push({ ...g })
      }
    }

    // When the job is still running, hide steps that have not yet produced
    // any log output — they haven't started and showing them prematurely
    // is misleading.  Once the job reaches a terminal state every step is
    // always shown so the user can inspect empty / skipped steps.
    if (props.job.status === 'running') {
      return result.filter((g) => g.lines.length > 0)
    }

    return result
  }

  // Fallback: no step metadata — drive from log line groups.
  return props.stepGroups.map((g) => {
    const fromId = stepById.get(g.stepId)
    if (fromId) {
      return { ...g, stepName: fromId }
    }
    const fromIndex = stepByIndex.get(g.stepId)
    if (fromIndex) {
      return { ...g, stepName: fromIndex }
    }
    if (/^\d+$/.test(g.stepId)) {
      return { ...g, stepName: `Step ${g.stepId}` }
    }
    return g
  })
})

// Resolve step metadata (type, run, script, with) for a given step group.
// Matches by step id (explicit or numeric 1-based index) and by step name.
interface StepMeta {
  type: string
  run?: string
  script?: string
  with?: Record<string, any>
}
const stepMeta = computed(() => {
  const byId = new Map<string, StepMeta>()
  const byIndex = new Map<string, StepMeta>()
  const byName = new Map<string, StepMeta>()
  if (props.job?.steps) {
    for (let i = 0; i < props.job.steps.length; i++) {
      const step = props.job.steps[i]!
      const meta: StepMeta = { type: step.type || 'run', run: step.run, script: step.script, with: step.with }
      if (step.id) byId.set(step.id, meta)
      if (step.name) byName.set(step.name, meta)
      byIndex.set(String(i + 1), meta)
    }
  }
  return { byId, byIndex, byName }
})

function resolveStepMeta(groupStepId: string): StepMeta | null {
  const { byId, byIndex, byName } = stepMeta.value
  return byId.get(groupStepId) ?? byIndex.get(groupStepId) ?? byName.get(groupStepId) ?? null
}

// Resolve step outputs for a step group (from job.step_outputs).
function resolveStepOutputs(groupStepId: string): Record<string, WorkflowOutputValue> | null {
  if (!props.job?.step_outputs) return null
  return props.job.step_outputs[groupStepId] ?? null
}

// Check if step outputs represent a release (has tag key).
function isReleaseOutputs(outputs: Record<string, WorkflowOutputValue>): boolean {
  return 'tag' in outputs
}

// Format a WorkflowOutputValue for display.
function formatOutputValue(val: WorkflowOutputValue): string {
  if (val.masked) return '***'
  return val.value
}
</script>

<template>
  <div class="flex flex-col h-full">
    <!-- Job header -->
    <div v-if="job" class="flex items-center gap-2 mb-3 shrink-0">
      <span class="text-sm font-semibold">{{ job.display_name || job.job_key }}</span>
      <Badge :variant="jobStatusVariant(job.status)" class="text-xs">
        {{ t(`repo.workflows.status.${job.status}`) }}
      </Badge>
    </div>
    <p v-else class="text-sm text-muted-foreground mb-3 shrink-0">
      {{ t('repo.workflows.runDetail.selectJob') }}
    </p>

    <!-- Loading -->
    <p v-if="loading" class="text-sm text-muted-foreground">
      {{ t('repo.workflows.job.logsLoading') }}
    </p>

    <!-- Error -->
    <p v-else-if="error" class="text-sm text-destructive">{{ error }}</p>

    <!-- Log viewer -->
    <div
      v-else-if="job"
      ref="logsContainer"
      class="flex-1 min-h-0 overflow-auto rounded-md bg-muted/30 border"
    >
      <!-- Auto-scroll toggle -->
      <div class="sticky top-0 z-10 flex items-center justify-end px-3 py-1.5 bg-muted/50 border-b backdrop-blur-sm">
        <label class="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer">
          <input
            :checked="autoScroll"
            type="checkbox"
            class="size-3.5 rounded"
            @change="emit('update:autoScroll', ($event.target as HTMLInputElement).checked)"
          />
          {{ t('agentSessions.actions.autoScroll') }}
        </label>
      </div>

      <div v-if="resolvedStepGroups.length === 0" class="p-4 text-sm text-muted-foreground">
        {{ t('repo.workflows.job.logsEmpty') }}
      </div>

      <!-- Step groups -->
      <div v-for="group in resolvedStepGroups" :key="group.stepId">
        <!-- Step header -->
        <button
          type="button"
          class="w-full flex items-center gap-2 px-3 py-2 text-left hover:bg-muted/50 transition-colors border-b border-border/50"
          @click="toggleStep(group.stepId)"
        >
          <span class="text-[10px] text-muted-foreground shrink-0 w-4 text-center">
            {{ expanded[group.stepId] ? '▾' : '▸' }}
          </span>
          <span class="text-xs font-medium truncate flex-1">
            {{ group.stepId === '_uncategorized' ? t('repo.workflows.step.uncategorized') : group.stepName }}
          </span>
          <Badge v-if="resolveStepMeta(group.stepId)?.type" variant="outline" class="text-[9px] px-1 py-0 shrink-0">
            {{ t(`repo.workflows.stepType.${resolveStepMeta(group.stepId)!.type}`) }}
          </Badge>
          <span class="text-[10px] text-muted-foreground shrink-0">
            {{ group.lines.length }} {{ t('repo.workflows.step.linesCount') }}
          </span>
        </button>

        <!-- Step body -->
        <div v-if="expanded[group.stepId]" class="bg-muted/20 border-b border-border/50">
          <!-- Command line for run / script steps -->
          <div
            v-if="resolveStepMeta(group.stepId)?.run"
            class="px-3 py-1.5 font-mono text-xs text-muted-foreground border-b border-border/30 bg-background/50"
          >
            <span class="select-none text-primary/70">$ </span>
            <span class="whitespace-pre-wrap break-all">{{ resolveStepMeta(group.stepId)!.run }}</span>
          </div>

          <!-- Release outputs panel -->
          <div
            v-if="resolveStepOutputs(group.stepId) && isReleaseOutputs(resolveStepOutputs(group.stepId)!)"
            class="px-3 py-2 border-b border-border/30 bg-background/50"
          >
            <div class="text-xs font-medium mb-1.5 text-muted-foreground">
              {{ t('repo.workflows.job.stepOutputs') }}
            </div>
            <div class="space-y-1 text-xs">
              <template v-for="(_val, key) in resolveStepOutputs(group.stepId)!" :key="key">
                <div v-if="key !== 'draft' && key !== 'published'" class="flex items-baseline gap-2">
                  <span class="text-muted-foreground shrink-0 w-20 text-right">{{ key }}</span>
                  <span v-if="key === 'release_url'" class="font-mono truncate">
                    <a :href="formatOutputValue(_val)" target="_blank" class="text-primary hover:underline">
                      {{ formatOutputValue(_val) }}
                    </a>
                  </span>
                  <span v-else class="font-mono">{{ formatOutputValue(_val) }}</span>
                </div>
              </template>
              <div class="flex items-baseline gap-2">
                <span class="text-muted-foreground shrink-0 w-20 text-right">{{ t('repo.workflows.job.colStatus') }}</span>
                <Badge :variant="resolveStepOutputs(group.stepId)!.published?.value === 'true' ? 'default' : 'secondary'" class="text-xs">
                  {{ resolveStepOutputs(group.stepId)!.published?.value === 'true'
                    ? t('release.published')
                    : t('release.draft') }}
                </Badge>
              </div>
            </div>
          </div>

          <!-- Log lines -->
          <div class="px-3 py-2">
            <LogLineGroup :lines="group.lines" />
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
