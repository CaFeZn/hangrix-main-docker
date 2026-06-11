<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  CheckCircle2,
  Circle,
  Loader2,
  XCircle,
  MinusCircle,
} from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import type { CheckItem } from '~/types/workflow'

const props = defineProps<{
  owner: string
  name: string
  issueNumber: number
  /** If set, fetch checks for this commit SHA (GET /api/repos/{owner}/{name}/commits/{sha}/checks).
   *  When absent the issue-level endpoint is used. */
  commitSha?: string
}>()

const { t } = useI18n()

const items = ref<CheckItem[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
// Track whether we've ever successfully loaded data so we can suppress
// the loading spinner on subsequent polls (avoids flickering between
// the check list and the loading state every 5 s).
const everLoaded = ref(false)

async function load() {
  error.value = null
  // Only show the loading spinner on first load; subsequent polls
  // update silently in the background so the UI doesn't flicker.
  if (!everLoaded.value) {
    loading.value = true
  }
  try {
    const url = props.commitSha
      ? `/api/repos/${props.owner}/${props.name}/commits/${props.commitSha}/checks`
      : `/api/repos/${props.owner}/${props.name}/issues/${props.issueNumber}/checks`
    const res = await $fetch<{ items: CheckItem[] }>(
      url,
      { credentials: 'include' },
    )
    items.value = res.items ?? []
    everLoaded.value = true
  } catch (e: any) {
    // Only surface errors on the initial load — transient poll
    // failures shouldn't clobber previously-successful data.
    if (!everLoaded.value) {
      error.value = e?.data?.error ?? t('issue.contributions.checksLoadFailed')
      items.value = []
    }
  } finally {
    loading.value = false
  }
}

// --- polling ---
const POLL_MS = 5_000
let timer: ReturnType<typeof setInterval> | null = null

function startPoll() {
  if (timer || typeof window === 'undefined') return
  timer = setInterval(() => {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    load()
  }, POLL_MS)
}

function stopPoll() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

onMounted(() => {
  load()
  startPoll()
})

onUnmounted(() => stopPoll())

// Re-load when issueNumber changes
watch(
  () => props.issueNumber,
  () => { load() },
)

// Re-load when commitSha changes (e.g. user switches between contributions)
watch(
  () => props.commitSha,
  () => { load() },
)

// --- helpers ---

/** Derive a display status from the backend's 3-state model
 *  (status: pending|running|completed + conclusion). */
function effectiveStatus(item: CheckItem): 'success' | 'failed' | 'running' | 'pending' | 'cancelled' | 'skipped' {
  if (item.status === 'completed') {
    if (item.conclusion === 'success') return 'success'
    if (item.conclusion === 'failure') return 'failed'
    if (item.conclusion === 'cancelled') return 'cancelled'
    if (item.conclusion === 'skipped') return 'skipped'
    return 'pending'
  }
  if (item.status === 'running') return 'running'
  return 'pending'
}

function statusIcon(s: ReturnType<typeof effectiveStatus>) {
  switch (s) {
    case 'success': return CheckCircle2
    case 'failed': return XCircle
    case 'running': return Loader2
    case 'cancelled': return MinusCircle
    case 'skipped': return MinusCircle
    default: return Circle
  }
}

function statusIconClass(s: ReturnType<typeof effectiveStatus>): string {
  switch (s) {
    case 'success': return 'text-emerald-500'
    case 'failed': return 'text-red-500'
    case 'running': return 'text-amber-500 animate-spin'
    case 'cancelled': return 'text-slate-400'
    case 'skipped': return 'text-slate-400'
    default: return 'text-slate-400'
  }
}

function statusBgClass(s: ReturnType<typeof effectiveStatus>): string {
  switch (s) {
    case 'success': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'failed': return 'bg-red-500/15 text-red-700 dark:text-red-300'
    case 'running': return 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
    case 'cancelled': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
    case 'skipped': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
    default: return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}

// Aggregate summary counts — skipped/cancelled are grouped together as neutral terminal states.
const summary = computed(() => {
  let success = 0
  let failed = 0
  let running = 0
  let pending = 0
  let cancelled = 0
  for (const item of items.value) {
    switch (effectiveStatus(item)) {
      case 'success': success++; break
      case 'failed': failed++; break
      case 'running': running++; break
      case 'cancelled': cancelled++; break
      case 'skipped': cancelled++; break
      default: pending++; break
    }
  }
  return { success, failed, running, pending, cancelled }
})

const overallVerdict = computed<'success' | 'failure' | 'running' | 'pending' | 'none'>(() => {
  if (items.value.length === 0) return 'none'
  if (summary.value.failed > 0) return 'failure'
  if (summary.value.running > 0 || summary.value.pending > 0) return 'running'
  // All items are terminal (success, cancelled, skipped) and none failed → success
  return 'success'
})
</script>

<template>
  <div class="space-y-3">
    <!-- Error state -->
    <Card v-if="error" class="gap-0 py-0">
      <CardContent class="p-4 text-sm text-destructive">
        {{ error }}
      </CardContent>
    </Card>

    <!-- Loading -->
    <Card v-else-if="loading" class="gap-0 py-0">
      <CardContent class="flex items-center gap-2 p-4 text-sm text-muted-foreground">
        <Loader2 class="size-4 animate-spin" />
        {{ t('issue.contributions.checksLoading') }}
      </CardContent>
    </Card>

    <!-- Empty -->
    <Card v-else-if="items.length === 0" class="gap-0 py-0">
      <CardContent class="flex items-center gap-2 p-4 text-sm text-muted-foreground">
        <Circle class="size-4" />
        {{ commitSha ? t('issue.contributions.checksEmptyContribution') : t('issue.contributions.checksEmpty') }}
      </CardContent>
    </Card>

    <!-- Check list -->
    <template v-else>
      <!-- Summary strip -->
      <div class="flex flex-wrap items-center gap-2 text-xs">
        <!-- Overall verdict badge -->
        <Badge
          v-if="overallVerdict !== 'none'"
          :class="overallVerdict === 'success'
            ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
            : overallVerdict === 'failure'
              ? 'bg-red-500/15 text-red-700 dark:text-red-300'
              : overallVerdict === 'running'
                ? 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
                : 'bg-slate-500/15 text-slate-700 dark:text-slate-300'"
          variant="secondary"
        >
          <component
            :is="overallVerdict === 'success' ? CheckCircle2 : overallVerdict === 'failure' ? XCircle : overallVerdict === 'running' ? Loader2 : Circle"
            :class="overallVerdict === 'running' ? 'animate-spin' : ''"
            class="mr-1 size-3"
          />
          <template v-if="overallVerdict === 'success'">{{ t('issue.contributions.checksAllPassed') }}</template>
          <template v-else-if="overallVerdict === 'failure'">{{ t('issue.contributions.checksSomeFailed') }}</template>
          <template v-else-if="overallVerdict === 'running'">{{ t('issue.contributions.checksRunning') }}</template>
          <template v-else>{{ t('issue.contributions.checksPending') }}</template>
        </Badge>

        <!-- Count chips -->
        <span v-if="summary.success > 0" class="inline-flex items-center gap-1">
          <CheckCircle2 class="size-3 text-emerald-500" />
          <span class="text-emerald-700 dark:text-emerald-300">{{ summary.success }}</span>
        </span>
        <span v-if="summary.failed > 0" class="inline-flex items-center gap-1">
          <XCircle class="size-3 text-red-500" />
          <span class="text-red-700 dark:text-red-300">{{ summary.failed }}</span>
        </span>
        <span v-if="summary.running > 0" class="inline-flex items-center gap-1">
          <Loader2 class="size-3 text-amber-500 animate-spin" />
          <span class="text-amber-700 dark:text-amber-300">{{ summary.running }}</span>
        </span>
        <span v-if="summary.pending > 0 || summary.cancelled > 0" class="inline-flex items-center gap-1">
          <MinusCircle class="size-3 text-slate-400" />
          <span class="text-slate-500">{{ summary.pending + summary.cancelled }}</span>
        </span>
      </div>

      <!-- Check list -->
      <Card class="gap-0 py-0">
        <CardContent class="p-0">
          <ul class="divide-y">
            <li v-for="item in items" :key="item.run_id">
              <NuxtLink
                v-if="item.run_id"
                :to="`/${owner}/${name}/workflows/${item.run_id}`"
                class="flex items-center gap-3 px-4 py-2.5 hover:bg-muted/30 transition-colors"
              >
                <!-- Status icon -->
                <component
                  :is="statusIcon(effectiveStatus(item))"
                  :class="statusIconClass(effectiveStatus(item))"
                  class="size-4 shrink-0"
                />

                <!-- Name + meta -->
                <div class="min-w-0 flex-1 space-y-0.5">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="text-sm font-medium truncate">{{ item.name }}</span>
                    <Badge :class="statusBgClass(effectiveStatus(item))" variant="secondary" class="text-xs">
                      {{ item.status === 'completed' ? item.conclusion || item.status : item.status }}
                    </Badge>
                  </div>
                </div>
              </NuxtLink>
              <!-- Non-linkable check (no run_id) -->
              <div
                v-else
                class="flex items-center gap-3 px-4 py-2.5"
              >
                <component
                  :is="statusIcon(effectiveStatus(item))"
                  :class="statusIconClass(effectiveStatus(item))"
                  class="size-4 shrink-0"
                />
                <div class="min-w-0 flex-1 space-y-0.5">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="text-sm font-medium truncate">{{ item.name }}</span>
                    <Badge :class="statusBgClass(effectiveStatus(item))" variant="secondary" class="text-xs">
                      {{ item.status === 'completed' ? item.conclusion || item.status : item.status }}
                    </Badge>
                  </div>
                </div>
              </div>
            </li>
          </ul>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
