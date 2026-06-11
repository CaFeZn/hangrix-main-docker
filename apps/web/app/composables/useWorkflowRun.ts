import { ref, onUnmounted, type Ref } from 'vue'
import type { WorkflowRunDetail } from '~/types/workflow'

/**
 * Fetch and poll a workflow run detail. Polls every 3s while the run is
 * in a non-terminal state. Caller is responsible for passing stable
 * owner/name/runID refs.
 */
export function useWorkflowRun(
  owner: Ref<string>,
  name: Ref<string>,
  runID: Ref<number>,
) {
  const detail = ref<WorkflowRunDetail | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  let timer: ReturnType<typeof setInterval> | null = null

  const isTerminal = (status: string) =>
    status === 'success' || status === 'failed' || status === 'cancelled'

  async function fetchOnce() {
    try {
      const data = await $fetch<WorkflowRunDetail>(
        `/api/repos/${owner.value}/${name.value}/workflow-runs/${runID.value}`,
        { credentials: 'include' },
      )
      detail.value = data
      error.value = null
      return data
    } catch (e: any) {
      error.value = e?.data?.error ?? 'Load failed'
      return null
    }
  }

  async function load() {
    loading.value = true
    error.value = null
    try {
      const data = await fetchOnce()
      if (data && !isTerminal(data.run.status)) {
        startPolling()
      }
    } finally {
      loading.value = false
    }
  }

  function startPolling() {
    stopPolling()
    timer = setInterval(async () => {
      const data = await fetchOnce()
      if (data && isTerminal(data.run.status)) {
        stopPolling()
      }
    }, 3000)
  }

  function stopPolling() {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  onUnmounted(stopPolling)

  return { detail, loading, error, load, stopPolling }
}
