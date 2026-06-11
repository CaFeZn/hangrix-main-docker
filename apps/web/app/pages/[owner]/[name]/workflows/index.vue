<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Play, Zap } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import Pagination from '@/components/ui/pagination/Pagination.vue'
import type { WorkflowRun, WorkflowRunListResp, WorkflowRunStatus } from '~/types/workflow'
import { relativeTime } from '~/utils/time'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

useHead({ title: () => `${owner.value}/${name.value} · ${t('repo.workflows.title')} - ${t('app.name')}` })

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.workflows.title') },
  ]
})

// ── Constants ──
// Must stay within the backend workflow-runs handler's limit cap (currently 200).
const PAGE_SIZE_OPTIONS = [20, 50, 100] as const
const DEFAULT_PAGE_SIZE = 20
const DEFAULT_PAGE = 1

// 'agent' is a special view showing internal _agent workflow runs.
// All other values are status filters for the regular (user) workflow runs.
const tabValues = ['all', 'pending', 'running', 'success', 'failed', 'cancelled', 'agent'] as const
type TabValue = typeof tabValues[number]

function parseTab(s: string | undefined): TabValue {
  if (s && (tabValues as readonly string[]).includes(s)) return s as TabValue
  return 'all'
}

function parsePage(s: string | undefined): number {
  const n = Number(s)
  return Number.isFinite(n) && n >= 1 ? Math.floor(n) : DEFAULT_PAGE
}

function parsePageSize(s: string | undefined): number {
  const n = Number(s)
  if (Number.isFinite(n) && (PAGE_SIZE_OPTIONS as readonly number[]).includes(n)) return n
  return DEFAULT_PAGE_SIZE
}

// ── URL query is the single source of truth ──
// The 'agent' tab is encoded as ?kind=agent; all other tabs use ?status=<value>.
const tab = computed<TabValue>(() => {
  if (route.query.kind === 'agent') return 'agent'
  return parseTab(String(route.query.status ?? ''))
})
const page = computed(() => parsePage(String(route.query.page ?? '')))
const pageSize = computed(() => parsePageSize(String(route.query.page_size ?? '')))

const offset = computed(() => (page.value - 1) * pageSize.value)
const limit = computed(() => pageSize.value)

function updateQuery(patch: Record<string, string | undefined>) {
  const next: Record<string, any> = { ...route.query }
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === '' || v === defaultQueryValue(k)) {
      delete next[k]
    } else {
      next[k] = v
    }
  }
  router.replace({ query: next })
}

function defaultQueryValue(key: string): string | undefined {
  switch (key) {
    case 'status': return undefined // 'all' is represented by absence
    case 'kind': return undefined // non-agent (user workflows) is represented by absence
    case 'page': return String(DEFAULT_PAGE)
    case 'page_size': return String(DEFAULT_PAGE_SIZE)
    default: return undefined
  }
}

function setTab(v: string | number) {
  const parsed = parseTab(String(v))
  if (parsed === 'agent') {
    // Agent tab: set kind=agent, clear status and page.
    updateQuery({ kind: 'agent', status: undefined, page: undefined })
  } else {
    // Status-based tab: clear kind, set status (or clear if 'all').
    updateQuery({ kind: undefined, status: parsed === 'all' ? undefined : parsed, page: undefined })
  }
}

// ── Data loading ──
const items = ref<WorkflowRun[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

let loadSeq = 0

async function load() {
  // Sanitize URL: strip non-whitelist page_size, default values, and non-numeric page values.
  // All corrections are batched into a single updateQuery call; otherwise the watch on computed
  // page/pageSize/tab won't re-fire after sanitization (the computed values stay the same).
  const patch: Record<string, string | undefined> = {}
  const rawPs = String(route.query.page_size ?? '')
  if (rawPs !== '' && (!(PAGE_SIZE_OPTIONS as readonly number[]).includes(Number(rawPs)) || Number(rawPs) === DEFAULT_PAGE_SIZE)) {
    patch.page_size = undefined
  }
  const rawPg = String(route.query.page ?? '')
  if (rawPg !== '' && (parsePage(rawPg) === DEFAULT_PAGE || rawPg !== String(parsePage(rawPg)))) {
    patch.page = undefined
  }
  const rawSt = String(route.query.status ?? '')
  if (rawSt !== '' && parseTab(rawSt) === 'all') {
    patch.status = undefined
  }
  if (Object.keys(patch).length > 0) {
    updateQuery(patch)
    // Fall through — computed page/pageSize/tab already reflect the sanitized
    // values, so the following fetch uses correct offset/limit/status.
  }

  const seq = ++loadSeq
  loading.value = true
  error.value = null
  try {
    const query: Record<string, any> = { limit: limit.value, offset: offset.value }
    if (tab.value === 'agent') {
      // Agent tab: fetch _agent internal workflow runs.
      query.kind = 'agent'
    } else if (tab.value !== 'all') {
      query.status = tab.value
    }
    const res = await $fetch<WorkflowRunListResp>(`/api/repos/${owner.value}/${name.value}/workflow-runs`, {
      credentials: 'include',
      query,
    })
    if (seq !== loadSeq) return // stale
    items.value = res.items ?? []
    total.value = res.total
    // Out-of-bounds fallback: if page exceeds the last page, reset to page 1.
    const maxPage = Math.max(1, Math.ceil(res.total / pageSize.value))
    if (page.value > maxPage) {
      updateQuery({ page: maxPage === 1 ? undefined : String(maxPage) })
      return
    }
  } catch (e: any) {
    if (seq !== loadSeq) return // stale
    error.value = e?.data?.error ?? t('repo.workflows.listFailed')
    items.value = []
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

watch([page, pageSize, tab], load, { immediate: true })

// ── Pagination event handlers ──
function onOffset(v: number) {
  const p = Math.floor(v / pageSize.value) + 1
  updateQuery({ page: p === DEFAULT_PAGE ? undefined : String(p) })
}

function onLimit(v: number) {
  updateQuery({ page_size: v === DEFAULT_PAGE_SIZE ? undefined : String(v), page: undefined })
}

// ── Display helpers ──
function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}

function shortSha(s: string) { return s.slice(0, 7) }

function statusVariant(s: WorkflowRunStatus) {
  switch (s) {
    case 'success': return 'default' as const
    case 'failed': return 'destructive' as const
    case 'running': return 'default' as const
    case 'cancelled': return 'outline' as const
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

// ── Dispatch dialog ──
// Minimal dispatch form — no definitions endpoint exists yet, so the user
// enters the workflow name and optional inputs manually.
interface DispatchInput {
  id: number
  name: string
  value: string
}

const dispatchOpen = ref(false)
const dispatchWorkflowName = ref('')
const dispatchRef = ref('')
const dispatchInputs = ref<DispatchInput[]>([])
const dispatchError = ref<string | null>(null)
const dispatchSending = ref(false)

let nextInputId = 0

function resetDispatchForm() {
  dispatchWorkflowName.value = ''
  dispatchRef.value = ''
  dispatchInputs.value = []
  nextInputId = 0
}

const canDispatch = computed(() => dispatchWorkflowName.value.trim().length > 0)

function addDispatchInput() {
  dispatchInputs.value.push({ id: nextInputId++, name: '', value: '' })
}

function removeDispatchInput(id: number) {
  dispatchInputs.value = dispatchInputs.value.filter(di => di.id !== id)
}

async function openDispatch() {
  dispatchOpen.value = true
  dispatchError.value = null
  resetDispatchForm()
}

async function onDispatch() {
  if (!dispatchWorkflowName.value.trim()) return
  dispatchSending.value = true
  dispatchError.value = null
  try {
    const inputs = dispatchInputs.value
      .filter(di => di.name.trim())
      .map(di => ({ name: di.name.trim(), value: di.value }))
    const body: { workflow_name: string; ref?: string; inputs: { name: string; value: string }[] } = {
      workflow_name: dispatchWorkflowName.value.trim(),
      inputs,
    }
    if (dispatchRef.value.trim()) body.ref = dispatchRef.value.trim()
    await $fetch(`/api/repos/${owner.value}/${name.value}/workflow-runs`, {
      method: 'POST',
      credentials: 'include',
      body,
    })
    dispatchOpen.value = false
    // eslint-disable-next-line no-alert
    window.alert(t('repo.workflows.dispatchSuccess'))
    load()
  } catch (e: any) {
    dispatchError.value = e?.data?.error ?? t('repo.workflows.dispatchFailed')
  } finally {
    dispatchSending.value = false
  }
}
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('repo.workflows.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('repo.workflows.subtitle') }}
        </p>
      </div>
      <Button v-if="tab !== 'agent'" @click="openDispatch">
        <Zap class="size-4" />
        {{ t('repo.workflows.dispatch') }}
      </Button>
    </header>

    <Tabs :model-value="tab" class="space-y-4" @update:model-value="setTab">
      <TabsList>
        <TabsTrigger value="all">
          {{ t('issue.filters.all') }}
        </TabsTrigger>
        <TabsTrigger value="pending">
          {{ t('repo.workflows.status.pending') }}
        </TabsTrigger>
        <TabsTrigger value="running">
          {{ t('repo.workflows.status.running') }}
        </TabsTrigger>
        <TabsTrigger value="success">
          {{ t('repo.workflows.status.success') }}
        </TabsTrigger>
        <TabsTrigger value="failed">
          {{ t('repo.workflows.status.failed') }}
        </TabsTrigger>
        <TabsTrigger value="cancelled">
          {{ t('repo.workflows.status.cancelled') }}
        </TabsTrigger>
        <TabsTrigger value="agent">
          {{ t('repo.workflows.agentTab') }}
        </TabsTrigger>
      </TabsList>

      <p v-if="error" class="text-sm text-destructive">
        {{ error }}
      </p>

      <Card class="gap-0 py-0">
        <CardContent class="p-0">
          <p v-if="loading" class="p-4 text-sm text-muted-foreground">
            {{ t('common.loading') }}
          </p>
          <p v-else-if="items.length === 0" class="p-6 text-center text-sm text-muted-foreground">
            {{ tab === 'agent' ? t('repo.workflows.agentEmpty') : t('repo.workflows.empty') }}
          </p>
          <ul v-else class="divide-y">
            <li v-for="run in items" :key="run.id" class="hover:bg-muted/30">
              <NuxtLink
                :to="`/${owner}/${name}/workflows/${run.id}`"
                class="flex items-start gap-3 px-4 py-3"
              >
                <Play class="mt-1 size-4 shrink-0 text-muted-foreground" />
                <div class="min-w-0 flex-1 space-y-1">
                  <div class="flex flex-wrap items-center gap-2">
                    <span class="truncate text-sm font-medium">{{ run.workflow_name }}</span>
                    <Badge :variant="statusVariant(run.status)">
                      {{ t(`repo.workflows.status.${run.status}`) }}
                    </Badge>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    {{ t(`repo.workflows.event.${run.event_name}`) }}
                    <span class="mx-1">·</span>
                    <code class="font-mono text-[10px]">{{ run.ref }}</code>
                    <span class="mx-1">·</span>
                    <code class="font-mono text-[10px]">{{ shortSha(run.commit_sha) }}</code>
                    <span class="mx-1">·</span>
                    {{ rel(run.created_at) }}
                    <template v-if="run.started_at">
                      <span class="mx-1">·</span>
                      {{ duration(run.started_at, run.finished_at) }}
                    </template>
                  </p>
                </div>
              </NuxtLink>
            </li>
          </ul>
        </CardContent>
      </Card>

      <Pagination
        v-if="!error"
        :total="total"
        :offset="offset"
        :limit="limit"
        :page-size-options="PAGE_SIZE_OPTIONS"
        @update:offset="onOffset"
        @update:limit="onLimit"
      />
    </Tabs>

    <!-- Dispatch dialog -->
    <Dialog v-model:open="dispatchOpen">
      <DialogContent class="max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('repo.workflows.dispatchTitle') }}</DialogTitle>
          <DialogDescription>
            {{ t('repo.workflows.dispatchSubtitle') }}
          </DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <!-- Workflow name -->
          <div class="space-y-2">
            <Label for="dispatch-workflow-name">{{ t('repo.workflows.colName') }}</Label>
            <Input
              id="dispatch-workflow-name"
              v-model="dispatchWorkflowName"
              :placeholder="t('repo.workflows.dispatchNamePlaceholder')"
              class="h-8 text-sm"
            />
          </div>

          <!-- Ref -->
          <div class="space-y-2">
            <Label for="dispatch-ref">{{ t('repo.workflows.dispatchRef') }}</Label>
            <Input id="dispatch-ref" v-model="dispatchRef" :placeholder="t('repo.workflows.dispatchRefPlaceholder')" />
            <p class="text-xs text-muted-foreground">
              {{ t('repo.workflows.dispatchRefHint') }}
            </p>
          </div>

          <!-- Inputs -->
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <Label>{{ t('repo.workflows.dispatchInputLabel') }}</Label>
              <Button variant="ghost" size="sm" class="h-6 text-xs" @click="addDispatchInput">
                {{ t('repo.workflows.dispatchAddInput') }}
              </Button>
            </div>
            <div
              v-for="di in dispatchInputs"
              :key="di.id"
              class="flex items-start gap-2"
            >
              <Input
                v-model="di.name"
                :placeholder="t('repo.workflows.dispatchInputNamePlaceholder')"
                class="h-8 flex-1 text-sm"
              />
              <Input
                v-model="di.value"
                :placeholder="t('repo.workflows.dispatchInputValuePlaceholder')"
                class="h-8 flex-1 text-sm"
              />
              <Button
                variant="ghost"
                size="icon"
                class="size-8 shrink-0 text-muted-foreground hover:text-destructive"
                @click="removeDispatchInput(di.id)"
              >
                &times;
              </Button>
            </div>
          </div>

          <p v-if="dispatchError" class="text-sm text-destructive">{{ dispatchError }}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="dispatchOpen = false">
            {{ t('common.cancel') }}
          </Button>
          <Button
            :disabled="!canDispatch || dispatchSending"
            @click="onDispatch"
          >
            {{ dispatchSending ? t('common.submitting') : t('repo.workflows.dispatchSubmit') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
