import { computed, onUnmounted, ref, type Ref } from 'vue'
import type { WorkflowJobLogLine, WorkflowJobLogsResp } from '~/types/workflow'

export interface StepLogGroup {
  stepId: string
  stepName: string
  lines: WorkflowJobLogLine[]
}

/**
 * Fetch and poll logs for a single workflow job. Groups lines by step_id
 * client-side. Supports incremental fetching via ?since= while polling.
 */
export function useWorkflowLogs(
  owner: Ref<string>,
  name: Ref<string>,
  runID: Ref<number>,
  jobID: Ref<number | null>,
  isRunning: Ref<boolean>,
) {
  const allLines = ref<WorkflowJobLogLine[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  let timer: ReturnType<typeof setInterval> | null = null

  /** Log lines grouped by step_id. Lines with null step_id go under "_uncategorized". */
  const stepGroups = computed<StepLogGroup[]>(() => {
    const groups = new Map<string, WorkflowJobLogLine[]>()
    for (const line of allLines.value) {
      const key = line.step_id || '_uncategorized'
      if (!groups.has(key)) groups.set(key, [])
      groups.get(key)!.push(line)
    }
    // Preserve insertion order (first seen step_id wins)
    const seen = new Set<string>()
    const result: StepLogGroup[] = []
    for (const line of allLines.value) {
      const key = line.step_id || '_uncategorized'
      if (!seen.has(key)) {
        seen.add(key)
        result.push({ stepId: key, stepName: key, lines: groups.get(key)! })
      }
    }
    return result
  })

  /** ID of the last log line fetched, for incremental polling. */
  const lastLineID = computed(() => {
    const lines = allLines.value
    if (lines.length === 0) return 0
    const last = lines[lines.length - 1]
    return last ? last.id : 0
  })

  async function fetchOnce(since = 0) {
    if (jobID.value === null) return
    try {
      const params = new URLSearchParams()
      if (since > 0) params.set('since', String(since))
      const qs = params.toString()
      const url = `/api/repos/${owner.value}/${name.value}/workflow-runs/${runID.value}/jobs/${jobID.value}/logs${qs ? '?' + qs : ''}`
      const res = await $fetch<WorkflowJobLogsResp>(url, { credentials: 'include' })
      if (since > 0) {
        // Incremental — append new lines
        const existingIds = new Set(allLines.value.map((l) => l.id))
        for (const line of res.lines ?? []) {
          if (!existingIds.has(line.id)) {
            allLines.value.push(line)
          }
        }
      } else {
        allLines.value = res.lines ?? []
      }
      error.value = null
    } catch (e: any) {
      error.value = e?.data?.error ?? 'Failed to load logs'
    }
  }

  async function load() {
    if (jobID.value === null) return
    loading.value = true
    error.value = null
    allLines.value = []
    try {
      await fetchOnce(0)
      if (isRunning.value) startPolling()
    } finally {
      loading.value = false
    }
  }

  function startPolling() {
    stopPolling()
    timer = setInterval(async () => {
      await fetchOnce(lastLineID.value)
    }, 3000)
  }

  function stopPolling() {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  onUnmounted(stopPolling)

  return { allLines, stepGroups, loading, error, load, stopPolling }
}
