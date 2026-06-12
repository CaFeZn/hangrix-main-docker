<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { Project } from '~/types/project'

const { t } = useI18n()

setBreadcrumbs(() => [
  { label: t('project.title'), to: '/projects' },
  { label: t('project.create') },
])
useHead({ title: () => `${t('project.createTitle')} - ${t('app.name')}` })

const router = useRouter()
const { user, refresh: refreshUser } = useCurrentUser()
const { orgs, refresh: refreshOrgs } = useMyOrgs()

const SELF = '__self__'
const ownerOptions = computed(() => [
  { value: SELF, label: user.value?.username ? `@${user.value.username}` : t('project.ownerSelfFallback') },
  ...((orgs.value ?? []).map(o => ({ value: o.name, label: o.name }))),
])

const form = ref({
  owner: SELF,
  name: '',
  description: '',
  visibility: 'private',
  architecture: '',
  module_boundaries: '',
})
const submitting = ref(false)
const error = ref<string | null>(null)

async function submit() {
  error.value = null
  submitting.value = true
  try {
    const body: Record<string, any> = { ...form.value }
    if (!body.owner || body.owner === SELF) delete body.owner
    const project = await $fetch<Project>('/api/projects', {
      method: 'POST',
      credentials: 'include',
      body,
    })
    router.push(`/projects/${project.owner_name}/${project.name}`)
  } catch (e: any) {
    error.value = e?.data?.error ?? t('project.createFailed')
  } finally {
    submitting.value = false
  }
}

onMounted(async () => {
  if (!user.value) await refreshUser()
  await refreshOrgs()
})
</script>

<template>
  <div class="mx-auto w-full max-w-4xl space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">{{ t('project.createTitle') }}</h1>
      <p class="text-sm text-muted-foreground">
        {{ t('project.createSubtitle') }}
      </p>
    </header>

    <Card>
      <form @submit.prevent="submit">
        <CardHeader>
          <CardTitle>{{ t('project.cardTitle') }}</CardTitle>
          <CardDescription>{{ t('project.cardDescription') }}</CardDescription>
        </CardHeader>
        <CardContent class="space-y-4">
          <div class="space-y-2">
            <Label>{{ t('project.owner') }}</Label>
            <Select v-model="form.owner">
              <SelectTrigger class="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem v-for="o in ownerOptions" :key="o.value" :value="o.value">
                  {{ o.label }}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div class="space-y-2">
            <Label for="project-name">{{ t('project.name') }}</Label>
            <Input id="project-name" v-model="form.name" autocomplete="off" required pattern="[A-Za-z0-9_.-]+" />
          </div>

          <div class="space-y-2">
            <Label for="project-description">{{ t('project.description') }}</Label>
            <Input id="project-description" v-model="form.description" autocomplete="off" />
          </div>

          <div class="space-y-3">
            <Label>{{ t('project.visibility') }}</Label>
            <RadioGroup v-model="form.visibility" class="grid gap-3 sm:grid-cols-2">
              <div class="flex items-start gap-3 rounded-md border p-3">
                <RadioGroupItem id="project-private" value="private" class="mt-1" />
                <div>
                  <Label for="project-private" class="text-sm font-medium">{{ t('project.visibilityPrivate') }}</Label>
                  <p class="text-xs text-muted-foreground">{{ t('project.visibilityPrivateHint') }}</p>
                </div>
              </div>
              <div class="flex items-start gap-3 rounded-md border p-3">
                <RadioGroupItem id="project-public" value="public" class="mt-1" />
                <div>
                  <Label for="project-public" class="text-sm font-medium">{{ t('project.visibilityPublic') }}</Label>
                  <p class="text-xs text-muted-foreground">{{ t('project.visibilityPublicHint') }}</p>
                </div>
              </div>
            </RadioGroup>
          </div>

          <div class="space-y-2">
            <Label for="project-architecture">{{ t('project.architecture') }}</Label>
            <Textarea id="project-architecture" v-model="form.architecture" rows="6" />
          </div>

          <div class="space-y-2">
            <Label for="project-boundaries">{{ t('project.moduleBoundaries') }}</Label>
            <Textarea id="project-boundaries" v-model="form.module_boundaries" rows="6" />
          </div>

          <p v-if="error" class="text-sm text-destructive">{{ error }}</p>
        </CardContent>
        <CardFooter class="justify-end gap-2">
          <Button variant="outline" type="button" as-child>
            <NuxtLink to="/projects">{{ t('common.cancel') }}</NuxtLink>
          </Button>
          <Button type="submit" :disabled="submitting">
            {{ submitting ? t('project.submitting') : t('project.submit') }}
          </Button>
        </CardFooter>
      </form>
    </Card>
  </div>
</template>
