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

const route = useRoute()
const owner = computed(() => String(route.params.owner))
const name = computed(() => String(route.params.name))

setBreadcrumbs(() => [
  { label: 'Projects', to: '/projects' },
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
    error.value = e?.data?.error ?? 'Failed to load project'
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
    actionError.value = e?.data?.error ?? 'Failed to link repo'
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
    actionError.value = e?.data?.error ?? 'Failed to link issue'
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
    actionError.value = e?.data?.error ?? 'Failed to create proposal'
  }
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
            {{ project.visibility }}
          </Badge>
        </div>
        <p class="text-sm text-muted-foreground">
          {{ project?.description || 'Multi-repo project space' }}
        </p>
      </div>
      <Button variant="outline" as-child>
        <NuxtLink to="/projects">
          Projects
        </NuxtLink>
      </Button>
    </header>

    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
    <p v-if="loading && !project" class="text-sm text-muted-foreground">Loading...</p>

    <template v-if="project">
      <section class="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Boxes class="size-4" />
              Architecture
            </CardTitle>
          </CardHeader>
          <CardContent>
            <pre class="whitespace-pre-wrap text-sm text-muted-foreground">{{ project.architecture || 'No architecture notes yet.' }}</pre>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <GitBranch class="size-4" />
              Module boundaries
            </CardTitle>
          </CardHeader>
          <CardContent>
            <pre class="whitespace-pre-wrap text-sm text-muted-foreground">{{ project.module_boundaries || 'No module boundaries yet.' }}</pre>
          </CardContent>
        </Card>
      </section>

      <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>

      <section class="grid gap-4 xl:grid-cols-3">
        <Card class="xl:col-span-2">
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <GitBranch class="size-4" />
              Repositories
            </CardTitle>
            <CardDescription>{{ project.repos?.length ?? 0 }} linked repositories</CardDescription>
          </CardHeader>
          <CardContent class="space-y-3">
            <div v-for="r in project.repos" :key="r.id" class="flex items-center justify-between gap-3 rounded-md border p-3">
              <div class="min-w-0">
                <NuxtLink :to="`/${r.owner_name}/${r.repo_name}`" class="truncate font-medium hover:underline">
                  {{ r.owner_name }} / {{ r.repo_name }}
                </NuxtLink>
                <p class="truncate text-xs text-muted-foreground">{{ r.role || 'repo' }} · {{ r.purpose || 'No purpose set' }}</p>
              </div>
            </div>
            <p v-if="!project.repos?.length" class="text-sm text-muted-foreground">No repositories linked.</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Plus class="size-4" />
              Link repo
            </CardTitle>
          </CardHeader>
          <CardContent>
            <form class="space-y-3" @submit.prevent="addRepo">
              <div class="grid grid-cols-2 gap-2">
                <div class="space-y-1">
                  <Label>Owner</Label>
                  <Input v-model="repoForm.owner" />
                </div>
                <div class="space-y-1">
                  <Label>Repo</Label>
                  <Input v-model="repoForm.name" />
                </div>
              </div>
              <div class="space-y-1">
                <Label>Role</Label>
                <Input v-model="repoForm.role" placeholder="core, runtime, ui..." />
              </div>
              <div class="space-y-1">
                <Label>Purpose</Label>
                <Input v-model="repoForm.purpose" />
              </div>
              <Button type="submit" class="w-full">Link</Button>
            </form>
          </CardContent>
        </Card>
      </section>

      <section class="grid gap-4 xl:grid-cols-3">
        <Card class="xl:col-span-2">
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <GitPullRequestArrow class="size-4" />
              Cross-repo issues
            </CardTitle>
            <CardDescription>{{ project.issue_links?.length ?? 0 }} tracked tasks</CardDescription>
          </CardHeader>
          <CardContent class="space-y-3">
            <div v-for="i in project.issue_links" :key="i.id" class="flex items-center justify-between gap-3 rounded-md border p-3">
              <div class="min-w-0">
                <NuxtLink :to="`/${i.owner_name}/${i.repo_name}/issues/${i.issue_number}`" class="truncate font-medium hover:underline">
                  {{ i.owner_name }} / {{ i.repo_name }} #{{ i.issue_number }}
                </NuxtLink>
                <p class="truncate text-xs text-muted-foreground">{{ i.kind }} · {{ i.issue_title }}</p>
              </div>
              <Badge :variant="i.issue_state === 'open' ? 'secondary' : 'outline'">{{ i.issue_state }}</Badge>
            </div>
            <p v-if="!project.issue_links?.length" class="text-sm text-muted-foreground">No linked issues.</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-base">Link issue</CardTitle>
          </CardHeader>
          <CardContent>
            <form class="space-y-3" @submit.prevent="linkIssue">
              <div class="grid grid-cols-2 gap-2">
                <Input v-model="issueForm.owner" placeholder="owner" />
                <Input v-model="issueForm.name" placeholder="repo" />
              </div>
              <Input v-model="issueForm.issue_number" type="number" min="1" placeholder="issue number" />
              <Input v-model="issueForm.kind" placeholder="kind" />
              <Input v-model="issueForm.summary" placeholder="summary" />
              <Button type="submit" class="w-full">Track</Button>
            </form>
          </CardContent>
        </Card>
      </section>

      <section class="grid gap-4 xl:grid-cols-3">
        <Card class="xl:col-span-2">
          <CardHeader>
            <CardTitle class="flex items-center gap-2 text-base">
              <Lightbulb class="size-4" />
              Repository proposals
            </CardTitle>
            <CardDescription>{{ project.repo_proposals?.length ?? 0 }} proposals</CardDescription>
          </CardHeader>
          <CardContent class="space-y-3">
            <div v-for="p in project.repo_proposals" :key="p.id" class="rounded-md border p-3">
              <div class="flex items-center justify-between gap-3">
                <div class="min-w-0 font-medium">{{ p.owner_name }} / {{ p.repo_name }}</div>
                <Badge variant="outline">{{ p.status }}</Badge>
              </div>
              <p class="mt-1 text-sm text-muted-foreground">{{ p.description || p.reason || 'No description' }}</p>
              <p v-if="p.module_boundary" class="mt-2 whitespace-pre-wrap text-xs text-muted-foreground">{{ p.module_boundary }}</p>
            </div>
            <p v-if="!project.repo_proposals?.length" class="text-sm text-muted-foreground">No repo proposals.</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle class="text-base">Propose repo</CardTitle>
          </CardHeader>
          <CardContent>
            <form class="space-y-3" @submit.prevent="createProposal">
              <div class="grid grid-cols-2 gap-2">
                <Input v-model="proposalForm.owner_name" placeholder="owner" />
                <Input v-model="proposalForm.repo_name" placeholder="repo" />
              </div>
              <Input v-model="proposalForm.description" placeholder="description" />
              <Textarea v-model="proposalForm.reason" rows="3" placeholder="reason" />
              <Textarea v-model="proposalForm.module_boundary" rows="4" placeholder="module boundary" />
              <Button type="submit" class="w-full">Propose</Button>
            </form>
          </CardContent>
        </Card>
      </section>
    </template>
  </div>
</template>
