<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import WorkflowRunHeader from '~/components/workflow/WorkflowRunHeader.vue'
import WorkflowJobList from '~/components/workflow/WorkflowJobList.vue'
import WorkflowStepLogViewer from '~/components/workflow/WorkflowStepLogViewer.vue'
import { useWorkflowRun } from '~/composables/useWorkflowRun'
import { useWorkflowLogs } from '~/composables/useWorkflowLogs'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
const id = computed(() => Number(route.params.id ?? 0))

const {
  detail,
  loading,
  error: runError,
  load: loadRun,
} = useWorkflowRun(owner, name, id)

// Job selection
const selectedJobId = ref<number | null>(null)

const selectedJob = computed(() =>
  detail.value?.jobs.find((j) => j.id === selectedJobId.value) ?? null,
)

// Determine if the selected job is still running
const isSelectedJobRunning = computed(() => {
  const job = selectedJob.value
  return job ? job.status === 'running' : false
})

// Logs for the selected job
const {
  stepGroups,
  loading: logsLoading,
  error: logsError,
  load: loadLogs,
  stopPolling: stopLogsPolling,
} = useWorkflowLogs(owner, name, id, selectedJobId, isSelectedJobRunning)

// Auto-scroll for log viewer
const logsAutoScroll = ref(true)

// Select first non-skipped job when detail loads
watch(detail, (d) => {
  if (!d) return
  // Stop any existing log polling
  stopLogsPolling()
  const firstJob = d.jobs.find((j) => j.status !== 'skipped') ?? d.jobs[0] ?? null
  if (firstJob) {
    selectedJobId.value = firstJob.id
  }
}, { immediate: true })

// Load logs when selected job changes
watch(selectedJobId, (jobId) => {
  if (jobId !== null) {
    loadLogs()
  }
})

useHead({ title: () => {
    const runName = detail.value?.run.workflow_name || `#${id.value}`
    return `${runName} · ${owner.value}/${name.value} · ${t('repo.workflows.title')} - ${t('app.name')}`
  } })

// --- Breadcrumbs ---
setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.workflows.title'), to: `${base}/workflows` },
    { label: detail.value?.run.workflow_name || `#${id.value}` },
  ]
})

onMounted(loadRun)

// --- Cancel run ---
const cancelling = ref(false)
const showCancelConfirm = ref(false)

async function cancelRun() {
  cancelling.value = true
  try {
    await $fetch(`/api/repos/${owner.value}/${name.value}/workflow-runs/${id.value}/cancel`, {
      method: 'POST',
      credentials: 'include',
    })
    showCancelConfirm.value = false
    await loadRun()
  } catch {
    // ignore
  } finally {
    cancelling.value = false
  }
}
</script>

<template>
  <div class="mx-auto w-full max-w-7xl space-y-4">
    <!-- Loading -->
    <p v-if="loading" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

    <!-- Error -->
    <div v-else-if="runError || !detail" class="space-y-2">
      <p class="text-sm text-destructive">{{ runError || t('repo.workflows.loadFailed') }}</p>
      <Button variant="outline" as-child>
        <NuxtLink :to="`/${owner}/${name}/workflows`">
          {{ t('repo.workflows.title') }}
        </NuxtLink>
      </Button>
    </div>

    <!-- Content -->
    <template v-else>
      <!-- Run header (full width) -->
      <WorkflowRunHeader :run="detail.run" />

      <!-- Cancel button -->
      <div v-if="detail.run.status === 'running'" class="flex items-center gap-2">
        <template v-if="showCancelConfirm">
          <span class="text-sm text-muted-foreground">{{ t('repo.workflows.cancelConfirm') }}</span>
          <Button variant="destructive" size="sm" :disabled="cancelling" @click="cancelRun">
            {{ cancelling ? t('common.submitting') : t('common.confirm') }}
          </Button>
          <Button variant="outline" size="sm" @click="showCancelConfirm = false">
            {{ t('common.cancel') }}
          </Button>
        </template>
        <Button v-else variant="outline" size="sm" @click="showCancelConfirm = true">
          {{ t('repo.workflows.cancel') }}
        </Button>
      </div>

      <!-- Two-panel layout -->
      <div class="grid grid-cols-1 md:grid-cols-[22%_78%] gap-4 min-h-0">
        <!-- Left panel: Job list -->
        <Card class="h-fit md:sticky md:top-4">
          <CardContent class="p-3">
            <WorkflowJobList
              :jobs="detail.jobs"
              :selected-job-id="selectedJobId"
              @select="selectedJobId = $event"
            />
          </CardContent>
        </Card>

        <!-- Right panel: Step log viewer -->
        <Card class="min-h-0">
          <CardContent class="p-4 h-full flex flex-col" style="max-height: calc(100vh - 260px);">
            <WorkflowStepLogViewer
              :job="selectedJob"
              :step-groups="stepGroups"
              :lines="[]"
              :loading="logsLoading"
              :error="logsError"
              :auto-scroll="logsAutoScroll"
              @update:auto-scroll="logsAutoScroll = $event"
            />
          </CardContent>
        </Card>
      </div>
    </template>
  </div>
</template>
