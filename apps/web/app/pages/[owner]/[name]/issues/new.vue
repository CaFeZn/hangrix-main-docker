<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import { CornerDownRight, GitBranch } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Form, FormControl, FormField, FormItem, FormMessage } from '@/components/ui/form'
import { Textarea } from '@/components/ui/textarea'
import SkillTitleInput from '@/components/issue/SkillTitleInput.vue'
import type { Issue } from '~/types/issue'
import type { Skill } from '~/types/skill'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.issues'), to: `${base}/issues` },
    { label: t('issue.newTitle') },
  ]
})
useHead({ title: () => `${t('issue.newTitle')} · ${owner.value}/${name.value} - ${t('app.name')}` })

const { repo, load: loadRepo } = useRepo(() => owner.value, () => name.value)

// parent query param drives sub-issue creation. Surfaces the parent's
// title/branch in the form so the user knows what they're branching from.
const parentNumber = computed(() => {
  const raw = Number(route.query.parent ?? 0)
  return Number.isFinite(raw) && raw > 0 ? raw : 0
})

const parent = ref<Issue | null>(null)
const submitting = ref(false)
const error = ref<string | null>(null)
const skills = ref<Skill[]>([])
const selectedSkill = ref<Skill | null>(null)

onMounted(async () => {
  await loadRepo()
  // Load skills for the slash autocomplete.
  try {
    const data = await $fetch<{ skills: Skill[] }>(
      `/api/repos/${owner.value}/${name.value}/skills`,
      { credentials: 'include' },
    )
    skills.value = data.skills
  } catch {
    skills.value = []
  }
  if (parentNumber.value > 0) {
    try {
      parent.value = await $fetch<Issue>(
        `/api/repos/${owner.value}/${name.value}/issues/${parentNumber.value}`,
        { credentials: 'include' },
      )
    } catch {
      parent.value = null
    }
  }
})

// Base branch is implicit — derived from parent (if any) or the repo
// default. The user does not pick it. Display-only.
const baseBranchLabel = computed(() => {
  if (parent.value) return parent.value.branch_name
  return repo.value?.default_branch ?? ''
})

const schema = computed(() => toTypedSchema(z.object({
  title: z.string().trim().min(1).max(200),
  body: z.string().max(50000),
})))

const initial = { title: '', body: '' }

function onSkillChange(payload: { slug: string; supplement: string } | null) {
  if (payload) {
    const found = skills.value.find((s) => s.slug === payload.slug)
    selectedSkill.value = found ?? null
  } else {
    selectedSkill.value = null
  }
}

async function onSubmit(values: any) {
  submitting.value = true
  error.value = null
  try {
    const body: Record<string, any> = { title: values.title, body: values.body }
    if (parentNumber.value > 0) body.parent_number = parentNumber.value
    if (selectedSkill.value) {
      const title = values.title.trim()
      const prefix = '/' + selectedSkill.value.slug
      let supplement = ''
      if (title.length > prefix.length + 1) {
        supplement = title.slice(prefix.length + 1).trim()
      }
      body.skill = {
        slug: selectedSkill.value.slug,
        supplement,
      }
      // Backend ignores body when skill is set.
      body.body = ''
    }
    const iss = await $fetch<Issue>(`/api/repos/${owner.value}/${name.value}/issues`, {
      method: 'POST',
      credentials: 'include',
      body,
    })
    router.push(`/${owner.value}/${name.value}/issues/${iss.number}`)
  } catch (e: any) {
    const code: string | undefined = e?.data?.code
    const slug = selectedSkill.value?.slug ?? ''
    const map: Record<string, string> = {
      skill_not_found:      t('issue.skill.errors.notFound', { slug }),
      skill_invalid:        t('issue.skill.errors.invalid'),
      skill_title_mismatch: t('issue.skill.errors.titleMismatch', { slug }),
      invalid_skill_slug:   t('issue.skill.errors.slug'),
    }
    error.value = map[code as string] ?? (e?.data?.error ?? t('issue.createFailed'))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('issue.newTitle') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('issue.newSubtitle') }}
      </p>
    </header>

    <!-- Context strip tells the user which base branch this issue will
         be created from. The issue's own working branch (issue/N) does
         not exist yet — it is assigned by the server on creation. There
         is no branch dropdown here because there is nothing to pick. -->
    <div class="space-y-2">
      <div class="flex flex-wrap items-center gap-2 rounded-md border bg-muted/30 px-3 py-2 text-sm">
        <CornerDownRight v-if="parent" class="size-4 text-muted-foreground" />
        <GitBranch v-else class="size-4 text-muted-foreground" />
        <template v-if="parent">
          <span class="text-muted-foreground">
            {{ t('issue.subIssueOf') }}
            <NuxtLink
              :to="`/${owner}/${name}/issues/${parent.number}`"
              class="font-medium text-foreground hover:underline"
            >
              #{{ parent.number }} {{ parent.title }}
            </NuxtLink>
            · {{ t('issue.base') }}:
          </span>
          <code class="font-mono text-xs">{{ baseBranchLabel }}</code>
        </template>
        <template v-else>
          <span class="text-muted-foreground">{{ t('issue.base') }}:</span>
          <code class="font-mono text-xs">{{ baseBranchLabel }}</code>
        </template>
      </div>
      <p class="text-xs text-muted-foreground">
        {{ t('issue.branchAutoNote') }}
      </p>
    </div>

    <Card class="gap-0 py-0">
      <CardContent class="p-4">
        <Form v-slot="{ handleSubmit }" :validation-schema="schema" :initial-values="initial">
          <form class="space-y-4" @submit="(e) => handleSubmit(e, onSubmit)">
            <FormField v-slot="{ componentField }" name="title">
              <FormItem>
                <FormControl>
                  <SkillTitleInput
                    :model-value="componentField.modelValue"
                    @update:model-value="(v: string) => (componentField as any)['onUpdate:modelValue'](v)"
                    :skills="skills"
                    :placeholder="t('issue.fields.titlePlaceholder')"
                    class="h-10 text-base"
                    @skill="onSkillChange"
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <FormField v-slot="{ componentField }" name="body">
              <FormItem>
                <template v-if="selectedSkill">
                  <div class="rounded-md border bg-muted/30 p-3 space-y-1">
                    <p class="text-xs text-muted-foreground">
                      {{ t('issue.skill.bodyOverride') }}
                    </p>
                    <pre class="text-sm whitespace-pre-wrap font-sans leading-relaxed">{{ selectedSkill.body }}</pre>
                  </div>
                </template>
                <FormControl v-else>
                  <Textarea
                    v-bind="componentField"
                    rows="16"
                    class="min-h-96 text-sm leading-relaxed"
                    :placeholder="t('issue.fields.bodyPlaceholder')"
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            </FormField>

            <p v-if="error" class="text-sm text-destructive">
              {{ error }}
            </p>

            <div class="flex justify-end gap-2">
              <Button
                variant="outline"
                type="button"
                @click="router.push(`/${owner}/${name}/issues${parentNumber > 0 ? `/${parentNumber}` : ''}`)"
              >
                {{ t('common.cancel') }}
              </Button>
              <Button type="submit" :disabled="submitting">
                {{ submitting ? t('issue.submitting') : t('issue.submit') }}
              </Button>
            </div>
          </form>
        </Form>
      </CardContent>
    </Card>
  </div>
</template>
