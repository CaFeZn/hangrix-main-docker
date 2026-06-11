<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Plus } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import Pagination from '@/components/ui/pagination/Pagination.vue'
import IssueRow from '@/components/issue/IssueRow.vue'
import { buildTree, flattenVisible, useIssueTree } from '~/composables/useIssueTree'
import type { Issue, IssueListResp } from '~/types/issue'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))
useHead({ title: () => `${owner.value}/${name.value} · ${t('issue.title')} - ${t('app.name')}` })

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.issues') },
  ]
})

const PER_PAGE = 20

const tabValues = ['all', 'open', 'merged', 'closed'] as const
type TabValue = typeof tabValues[number]

function parseTab(s: string | undefined): TabValue {
  if (s && (tabValues as readonly string[]).includes(s)) return s as TabValue
  return 'open'
}
function parsePage(p: string | undefined): number {
  const n = Number(p)
  return Number.isInteger(n) && n >= 1 ? n : 1
}

// URL is the source of truth for state and page
const tab = computed<TabValue>(() => parseTab(String(route.query.state ?? '')))
const page = computed(() => parsePage(String(route.query.page ?? '')))
const offset = computed(() => (page.value - 1) * PER_PAGE)

function setTab(v: string | number) {
  const parsed = parseTab(String(v))
  const query: Record<string, any> = {}
  if (parsed !== 'open') query.state = parsed
  router.replace({ query })
}
function setOffset(newOffset: number) {
  const newPage = Math.floor(newOffset / PER_PAGE) + 1
  const query: Record<string, any> = {}
  if (tab.value !== 'open') query.state = tab.value
  if (newPage > 1) query.page = String(newPage)
  router.replace({ query })
}

const items = ref<Issue[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

const { collapsed, toggle, collapseAll, expandAll } = useIssueTree()

const tree = computed(() => buildTree(items.value))
const visibleRows = computed(() => flattenVisible(tree.value, collapsed.value))

async function load() {
  loading.value = true
  error.value = null
  try {
    const query: Record<string, any> = { limit: PER_PAGE, offset: offset.value, view: 'tree' }
    if (tab.value !== 'all') query.state = tab.value
    const res = await $fetch<IssueListResp>(`/api/repos/${owner.value}/${name.value}/issues`, {
      credentials: 'include',
      query,
    })
    items.value = res.items ?? []
    total.value = res.total

    // Out-of-bounds page: redirect to page 1
    if (items.value.length === 0 && total.value > 0 && page.value > 1) {
      router.replace({ query: tab.value !== 'open' ? { state: tab.value } : {} })
    }

    // Default to collapsed for nodes that have children.
    collapseAll(tree.value)
  } catch (e: any) {
    error.value = e?.data?.error ?? t('issue.listFailed')
    items.value = []
  } finally {
    loading.value = false
  }
}

watch([tab, page], () => { load() }, { immediate: true })

function gotoNew() {
  router.push(`/${owner.value}/${name.value}/issues/new`)
}
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">
          {{ t('issue.title') }}
        </h1>
        <p class="text-sm text-muted-foreground">
          {{ t('issue.subtitle') }}
        </p>
      </div>
      <Button @click="gotoNew">
        <Plus class="size-4" />
        {{ t('issue.new') }}
      </Button>
    </header>

    <Tabs :model-value="tab" @update:model-value="setTab" class="space-y-4">
      <TabsList>
        <TabsTrigger value="open">
          {{ t('issue.filters.open') }}
        </TabsTrigger>
        <TabsTrigger value="merged">
          {{ t('issue.filters.merged') }}
        </TabsTrigger>
        <TabsTrigger value="closed">
          {{ t('issue.filters.closed') }}
        </TabsTrigger>
        <TabsTrigger value="all">
          {{ t('issue.filters.all') }}
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
            {{ t('issue.empty') }} —
            <NuxtLink :to="`/${owner}/${name}/issues/new`" class="underline">
              {{ t('issue.new') }}
            </NuxtLink>
          </p>
          <ul v-else class="divide-y">
            <IssueRow
              v-for="row in visibleRows"
              :key="`${row.issue.id}-${row.depth}`"
              :node="row"
              :owner="owner"
              :name="name"
              :collapsed="collapsed.has(row.issue.number)"
              :on-toggle="toggle"
            />
          </ul>
        </CardContent>
      </Card>
      <Pagination
        v-if="!loading && total > 0"
        :total="total"
        :offset="offset"
        :limit="PER_PAGE"
        @update:offset="setOffset"
      />
    </Tabs>
  </div>
</template>
