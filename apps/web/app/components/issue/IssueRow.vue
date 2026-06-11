<script setup lang="ts">
import { ChevronRight } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import IssueIndicators from './IssueIndicators.vue'
import type { IssueState, Issue } from '~/types/issue'
import type { TreeNode } from '~/composables/useIssueTree'
import { CircleDot, GitMerge, Lock } from 'lucide-vue-next'
import { relativeTime } from '~/utils/time'

const props = defineProps<{
  node: TreeNode
  owner: string
  name: string
  collapsed: boolean
  onToggle: (n: number) => void
}>()

const { t } = useI18n()
const iss = computed(() => props.node.issue)
const indent = computed(() => `${props.node.depth * 18}px`)
const hasChildren = computed(() => props.node.children.length > 0)

function badgeIcon(state: IssueState) {
  if (state === 'merged') return GitMerge
  if (state === 'closed') return Lock
  return CircleDot
}

function badgeClass(state: IssueState) {
  switch (state) {
    case 'open': return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
    case 'merged': return 'bg-violet-500/15 text-violet-700 dark:text-violet-300'
    case 'closed': return 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
  }
}

function rel(s?: string | null) {
  return relativeTime(s ?? null, t)
}
</script>

<template>
  <li class="group hover:bg-muted/30">
    <NuxtLink
      :to="`/${owner}/${name}/issues/${iss.number}`"
      class="flex items-start gap-2 px-4 py-3"
      :style="{ paddingLeft: `calc(var(--spacing, 1rem) + ${indent})` }"
    >
      <!-- Chevron / placeholder -->
      <button
        v-if="hasChildren"
        class="mt-1 size-4 shrink-0 flex items-center justify-center text-muted-foreground hover:text-foreground"
        :aria-label="collapsed ? t('issue.tree.expand') : t('issue.tree.collapse')"
        @click.stop.prevent="onToggle(iss.number)"
      >
        <ChevronRight
          class="size-3.5 transition-transform"
          :class="{ 'rotate-90': !collapsed }"
        />
      </button>
      <span v-else class="mt-1 size-4 shrink-0" />

      <!-- State icon -->
      <component :is="badgeIcon(iss.state)" class="mt-1 size-4 shrink-0 text-muted-foreground" />

      <!-- Content -->
      <div class="min-w-0 flex-1 space-y-1">
        <div class="flex flex-wrap items-center gap-2">
          <span class="truncate text-sm font-medium">{{ iss.title }}</span>
          <IssueIndicators :indicators="iss.indicators" />
          <Badge :class="badgeClass(iss.state)" variant="secondary">
            {{ t(`issue.state.${iss.state}`) }}
          </Badge>
        </div>
        <p class="text-xs text-muted-foreground">
          #{{ iss.number }}
          <template v-if="iss.parent_number === 0">
            · {{ t('issue.openedBy', { name: iss.author_username, time: rel(iss.created_at) }) }}
          </template>
          <template v-if="collapsed && hasChildren">
            · {{ t('issue.tree.childCount', { count: node.descendantCount }) }}
          </template>
        </p>
      </div>

      <code class="hidden font-mono text-xs text-muted-foreground sm:inline">
        {{ iss.branch_name }}
      </code>
    </NuxtLink>
  </li>
</template>
