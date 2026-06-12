<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ArrowRight, Boxes, Plus } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import type { Project, ProjectListResp } from '~/types/project'

const { t } = useI18n()

setBreadcrumbs(() => [{ label: t('project.title') }])
useHead({ title: () => `${t('project.title')} - ${t('app.name')}` })

const projects = ref<Project[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = null
  try {
    const res = await $fetch<ProjectListResp>('/api/projects', { credentials: 'include' })
    projects.value = res.items
    total.value = res.total
  } catch (e: any) {
    error.value = e?.data?.error ?? t('project.loadFailed')
  } finally {
    loading.value = false
  }
}

function visibilityLabel(value: string) {
  return value === 'public' ? t('project.visibilityPublic') : t('project.visibilityPrivate')
}

function ownerKindLabel(value: string) {
  return value === 'org' ? t('owner.kindOrg') : t('owner.kindUser')
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <header class="flex items-start justify-between gap-4">
      <div class="space-y-1">
        <h1 class="text-2xl font-semibold tracking-tight">{{ t('project.title') }}</h1>
        <p class="text-sm text-muted-foreground">
          {{ t('project.subtitle') }} · {{ t('common.total', { n: total }) }}
        </p>
      </div>
      <Button as-child>
        <NuxtLink to="/projects/new">
          <Plus class="size-4" />
          {{ t('project.create') }}
        </NuxtLink>
      </Button>
    </header>

    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
    <p v-if="loading && !projects.length" class="text-sm text-muted-foreground">{{ t('common.loading') }}</p>

    <section v-if="!loading && projects.length === 0" class="rounded-lg border border-dashed p-10 text-center">
      <Boxes class="mx-auto size-10 text-muted-foreground" />
      <h2 class="mt-4 text-lg font-medium">{{ t('project.empty') }}</h2>
      <p class="mt-1 text-sm text-muted-foreground">
        {{ t('project.emptyHint') }}
      </p>
      <Button class="mt-6" as-child>
        <NuxtLink to="/projects/new">
          <Plus class="size-4" />
          {{ t('project.create') }}
        </NuxtLink>
      </Button>
    </section>

    <section v-else class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <Card v-for="p in projects" :key="p.id" class="transition-shadow hover:shadow-md">
        <CardHeader>
          <div class="flex items-center justify-between gap-2">
            <CardTitle class="truncate text-base">
              <NuxtLink :to="`/projects/${p.owner_name}/${p.name}`" class="hover:underline">
                {{ p.owner_name }} / {{ p.name }}
              </NuxtLink>
            </CardTitle>
            <Badge :variant="p.visibility === 'private' ? 'outline' : 'secondary'">
              {{ visibilityLabel(p.visibility) }}
            </Badge>
          </div>
          <CardDescription class="line-clamp-2 min-h-[2.5rem]">
            {{ p.description || t('project.noDescription') }}
          </CardDescription>
        </CardHeader>
        <CardContent class="flex items-center justify-between text-xs text-muted-foreground">
          <span>{{ ownerKindLabel(p.owner_kind) }}</span>
          <NuxtLink :to="`/projects/${p.owner_name}/${p.name}`" class="inline-flex items-center gap-1 text-foreground hover:text-primary">
            <span>{{ t('project.open') }}</span>
            <ArrowRight class="size-3" />
          </NuxtLink>
        </CardContent>
      </Card>
    </section>
  </div>
</template>
