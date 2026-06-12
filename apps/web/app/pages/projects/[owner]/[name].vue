<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Boxes, GitBranch, GitPullRequestArrow, Lightbulb, Plus } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import type { Project } from '~/types/project'

const { t } = useI18n()
const route = useRoute()
const owner = computed(() => String(route.params.owner))
const name = computed(() => String(route.params.name))

setBreadcrumbs(() => [
  { label: t('project.title'), to: '/projects' },
  { label: `${owner.value}/${name.value}` },
])
useHead({ title: () => `${owner.value}/${name.value} - Hangrix` })

const project = ref<Project | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)

const repoForm = ref({ owner: '', name: '', purpose: '', role: '' })
const issueForm = ref({ owner: '', name: '', issue_number: '', kind: 'implementation', summary: '' })
const proposalForm = ref({ owner_name: '', repo_name: '', description: '', reason: '', module_boundary: '' })
const actionError = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    project.value = await $fetch<Project>(`/api/projects/${owner.value}/${name.value}`, { credentials: 'include' })
    if (!repoForm.value.owner) repoForm.value.owner = project.value.owner_name
    if (!proposalForm.value.owner_name) proposalForm.value.owner_name = project.value.owner_name
  } catch (e: any) {
    error.value = e?.data?.error ?? t('project.loadFailed')
  } finally {
    loading.value = false
  }
}

async function addRepo() {
  if (!repoForm.value.owner || !repoForm.value.name) return
  actionError.value = null
  try {
    await $fetch(`/api/projects/${owner.value}/${name.value}/repos`, {
      method: 'POST',
      credentials: 'include',
      body: repoForm.value,
    })
    repoForm.value = { owner: project.value?.owner_name ?? '', name: '', purpose: '', role: '' }
    await load()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('project.linkRepoFailed')
  }
}

async function linkIssue() {
  const issueNumber = Number(issueForm.value.issue_number)
  if (!issueForm.value.owner || !issueForm.value.name || !issueNumber) return
  actionError.value = null
  try {
    await $fetch(`/api/projects/${owner.value}/${name.value}/issue-links`, {
      method: 'POST',
      credentials: 'include',
      body: {
        owner: issueForm.value.owner,
        name: issueForm.value.name,
        issue_number: issueNumber,
        kind: issueForm.value.kind,
        summary: issueForm.value.summary,
      },
    })
    issueForm.value = { owner: '', name: '', issue_number: '', kind: 'implementation', summary: '' }
    await load()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('project.linkIssueFailed')
  }
}

async function createProposal() {
  if (!proposalForm.value.owner_name || !proposalForm.value.repo_name) return
  actionError.value = null
  try {
    await $fetch(`/api/projects/${owner.value}/${name.value}/repo-proposals`, {
      method: 'POST',
      credentials: 'include',
      body: proposalForm.value,
    })
    proposalForm.value = { owner_name: project.value?.owner_name ?? '', repo_name: '', description: '', reason: '', module_boundary: '' }
    await load()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? t('project.proposalFailed')
  }
}

function visibilityLabel(value: string) {
  return value === 'public' ? t('project.visibilityPublic') : t('project.visibilityPrivate')
}

function issueStateLabel(value: string) {
  switch (value) {
    case 'open':
      return t('project.issueStateOpen')
    case 'closed':
      return t('project.issueStateClosed')
    case 'merged':
      return t('project.issueStateMerged')
    default:
      return value || t('project.unknown')
  }
}

function proposalStatusLabel(value: string) {
  switch (value) {
    case 'pending':
      return t('project.proposalStatusPending')
    case 'approved':
      return t('project.proposalStatusApproved')
    case 'rejected':
      return t('project.proposalStatusRejected')
    case 'provisioned':
      return t('project.proposalStatusProvisioned')
    default:
      return value || t('project.unknown')
  }
}

function issueKindLabel(value: string) {
  return value === 'implementation' ? t('project.issueKindImplementation') : value
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="flex flex-wrap items-start justify-between gap-4">
      <div class="space-y-1">
        <div class="flex flex-wrap items-center gap-2">
          <h1 class="text-2xl font-semibold tracking-tight">
            {{ owner }} / {{ name }}
          </h1>
          <Badge v-if="project" :variant="project.visibility === 'private' ? 'outline' : 'secondary'">
            {{ visibilityLabel(project.visibility) }}
          </Badge>
        </div>
        <p class="text-sm text-muted-foreground">
          {{ project?.description || t('project.detailFallback') }}
        </p>
      </div>
      <Button variant="outline" as-child>
        <NuxtLink to="/projects">
          {{ t('project.title') }}
        </NuxtLink>
      </Button>
    </header>

    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
    <p v-if="loading && !project" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

    <template v-if="project">
      <section class="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Boxes class="size-4" />
              {{ t('project.architecture') }}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <pre class="whitespace-pre-wrap text-sm text-muted-foreground">{{ project.architecture || t('project.noArchitecture') }}</pre>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <GitBranch class="size-4" />
              {{ t('project.moduleBoundaries') }}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <pre class="whitespace-pre-wrap text-sm text-muted-foreground">{{ project.module_boundaries || t('project.noModuleBoundaries') }}</pre>
          </CardContent>
        </Card>
      </section>

      <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>

      <section class="grid gap-4 xl:grid-cols-3">
        <Card class="xl:col-span-2">
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <GitBranch class="size-4" />
              {{ t('project.repositories') }}
            </CardTitle>
            <CardDescription>{{ t('project.linkedRepositories', { n: project.repos?.length ?? 0 }) }}</CardDescription>
          </CardHeader>
          <CardContent class="space-y-3">
            <div v-for="r in project.repos" :key="r.id" class="flex items-center justify-between gap-3 rounded-md border p-3">
              <div class="min-w-0">
                <NuxtLink :to="`/${r.owner_name}/${r.repo_name}`" class="truncate font-medium hover:underline">
                  {{ r.owner_name }} / {{ r.repo_name }}
                </NuxtLink>
                <p class="truncate text-xs text-muted-foreground">{{ r.role || t('project.repoFallback') }} · {{ r.purpose || t('project.noPurpose') }}</p>
              </div>
            </div>
            <p v-if="!project.repos?.length" class="text-sm text-muted-foreground">{{ t('project.noRepositories') }}</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Plus class="size-4" />
              {{ t('project.linkRepo') }}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form class="space-y-3" @submit.prevent="addRepo">
              <div class="grid grid-cols-2 gap-2">
                <div class="space-y-1">
                  <Label>{{ t('project.owner') }}</Label>
                  <Input v-model="repoForm.owner" />
                </div>
                <div class="space-y-1">
                  <Label>{{ t('project.repo') }}</Label>
                  <Input v-model="repoForm.name" />
                </div>
              </div>
              <div class="space-y-1">
                <Label>{{ t('project.role') }}</Label>
                <Input v-model="repoForm.role" :placeholder="t('project.rolePlaceholder')" />
              </div>
              <div class="space-y-1">
                <Label>{{ t('project.purpose') }}</Label>
                <Input v-model="repoForm.purpose" />
              </div>
              <Button type="submit" class="w-full">{{ t('project.link') }}</Button>
            </form>
          </CardContent>
        </Card>
      </section>

      <section class="grid gap-4 xl:grid-cols-3">
        <Card class="xl:col-span-2">
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <GitPullRequestArrow class="size-4" />
              {{ t('project.crossRepoIssues') }}
            </CardTitle>
            <CardDescription>{{ t('project.trackedTasks', { n: project.issue_links?.length ?? 0 }) }}</CardDescription>
          </CardHeader>
          <CardContent class="space-y-3">
            <div v-for="i in project.issue_links" :key="i.id" class="flex items-center justify-between gap-3 rounded-md border p-3">
              <div class="min-w-0">
                <NuxtLink :to="`/${i.owner_name}/${i.repo_name}/issues/${i.issue_number}`" class="truncate font-medium hover:underline">
                  {{ i.owner_name }} / {{ i.repo_name }} #{{ i.issue_number }}
                </NuxtLink>
                <p class="truncate text-xs text-muted-foreground">{{ issueKindLabel(i.kind) }} · {{ i.issue_title }}</p>
              </div>
              <Badge :variant="i.issue_state === 'open' ? 'secondary' : 'outline'">{{ issueStateLabel(i.issue_state) }}</Badge>
            </div>
            <p v-if="!project.issue_links?.length" class="text-sm text-muted-foreground">{{ t('project.noIssueLinks') }}</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-base">{{ t('project.linkIssue') }}</CardTitle>
          </CardHeader>
          <CardContent>
            <form class="space-y-3" @submit.prevent="linkIssue">
              <div class="grid grid-cols-2 gap-2">
                <Input v-model="issueForm.owner" :placeholder="t('project.owner')" />
                <Input v-model="issueForm.name" :placeholder="t('project.repo')" />
              </div>
              <Input v-model="issueForm.issue_number" type="number" min="1" :placeholder="t('project.issueNumber')" />
              <Input v-model="issueForm.kind" :placeholder="t('project.kind')" />
              <Input v-model="issueForm.summary" :placeholder="t('project.summary')" />
              <Button type="submit" class="w-full">{{ t('project.track') }}</Button>
            </form>
          </CardContent>
        </Card>
      </section>

      <section class="grid gap-4 xl:grid-cols-3">
        <Card class="xl:col-span-2">
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Lightbulb class="size-4" />
              {{ t('project.repositoryProposals') }}
            </CardTitle>
            <CardDescription>{{ t('project.proposalCount', { n: project.repo_proposals?.length ?? 0 }) }}</CardDescription>
          </CardHeader>
          <CardContent class="space-y-3">
            <div v-for="p in project.repo_proposals" :key="p.id" class="rounded-md border p-3">
              <div class="flex items-center justify-between gap-3">
                <div class="min-w-0 font-medium">{{ p.owner_name }} / {{ p.repo_name }}</div>
                <Badge variant="outline">{{ proposalStatusLabel(p.status) }}</Badge>
              </div>
              <p class="mt-1 text-sm text-muted-foreground">{{ p.description || p.reason || t('project.noDescription') }}</p>
              <p v-if="p.module_boundary" class="mt-2 whitespace-pre-wrap text-xs text-muted-foreground">{{ p.module_boundary }}</p>
            </div>
            <p v-if="!project.repo_proposals?.length" class="text-sm text-muted-foreground">{{ t('project.noRepoProposals') }}</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-base">{{ t('project.proposeRepo') }}</CardTitle>
          </CardHeader>
          <CardContent>
            <form class="space-y-3" @submit.prevent="createProposal">
              <div class="grid grid-cols-2 gap-2">
                <Input v-model="proposalForm.owner_name" :placeholder="t('project.owner')" />
                <Input v-model="proposalForm.repo_name" :placeholder="t('project.repo')" />
              </div>
              <Input v-model="proposalForm.description" :placeholder="t('project.description')" />
              <Textarea v-model="proposalForm.reason" rows="3" :placeholder="t('project.reason')" />
              <Textarea v-model="proposalForm.module_boundary" rows="4" :placeholder="t('project.moduleBoundary')" />
              <Button type="submit" class="w-full">{{ t('project.submitProposal') }}</Button>
            </form>
          </CardContent>
        </Card>
      </section>
    </template>
  </div>
</template>
