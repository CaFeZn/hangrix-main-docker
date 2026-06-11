<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import { useForm } from 'vee-validate'
import * as z from 'zod'
import { Rocket, Sparkles } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'
import type { Release, ReleaseCreateReq, TagAnnotation } from '~/types/release'

definePageMeta({ layout: 'repo' })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const owner = computed(() => String(route.params.owner ?? ''))
const name = computed(() => String(route.params.name ?? ''))

useHead({ title: () => `${t('release.newTitle')} · ${owner.value}/${name.value} - ${t('app.name')}` })

setBreadcrumbs(() => {
  const base = `/${owner.value}/${name.value}`
  return [
    { label: owner.value, to: base },
    { label: name.value, to: base },
    { label: t('repo.tabs2.releases'), to: `${base}/releases` },
    { label: t('release.newTitle') },
  ]
})

const { refs, load: loadRefs } = useRepoRefs(() => owner.value, () => name.value)

onMounted(() => { loadRefs() })

const tags = computed(() => refs.value?.tags ?? [])

const schema = computed(() => toTypedSchema(z.object({
  tag_name: z.string().min(1, t('release.fields.tagRequired')),
  title: z.string().optional(),
  notes: z.string().optional(),
})))

const initialValues = {
  tag_name: '',
  title: '',
  notes: '',
}

type ReleaseFormValues = typeof initialValues

const createError = ref<string | null>(null)

const { handleSubmit, isSubmitting, values, setFieldValue } = useForm<ReleaseFormValues>({
  validationSchema: schema,
  initialValues,
})

// ---- Tag annotation autofill ----
const annotation = ref<TagAnnotation | null>(null)
const annotationLoading = ref(false)
const userEdited = ref(false)
const showAutofillHint = computed(() =>
  annotation.value?.kind === 'annotated' && !userEdited.value && !annotationLoading.value)

let fetchToken = 0

async function fetchTagAnnotation(tag: string) {
  if (!tag) {
    fetchToken += 1
    annotation.value = null
    annotationLoading.value = false
    userEdited.value = false
    setFieldValue('notes', '')
    return
  }

  annotation.value = null
  annotationLoading.value = true
  userEdited.value = false
  const token = ++fetchToken

  try {
    const res = await $fetch<TagAnnotation>(
      `/api/repos/${owner.value}/${name.value}/tags/${encodeURIComponent(tag)}/annotation`,
      { credentials: 'include' })
    if (token !== fetchToken) return

    annotation.value = res
    const nextNotes = res.kind === 'annotated' ? res.message : ''
    setFieldValue('notes', nextNotes)
  } catch {
    if (token !== fetchToken) return
    annotation.value = null
  } finally {
    if (token === fetchToken) {
      annotationLoading.value = false
    }
  }
}

watch(
  () => values.tag_name,
  tag => void fetchTagAnnotation(tag),
  { flush: 'post' },
)

const onCreate = handleSubmit(async (formValues) => {
  createError.value = null
  const body: ReleaseCreateReq = {
    tag_name: formValues.tag_name,
  }
  if (formValues.title?.trim()) body.title = formValues.title.trim()
  if (formValues.notes?.trim()) body.notes = formValues.notes.trim()

  try {
    const rel = await $fetch<Release>(`/api/repos/${owner.value}/${name.value}/releases`, {
      method: 'POST',
      credentials: 'include',
      body,
    })
    router.push(`/${owner.value}/${name.value}/releases/${rel.id}`)
  } catch (e: any) {
    createError.value = e?.data?.error ?? t('release.createFailed')
  }
})
</script>

<template>
  <div class="mx-auto w-full max-w-2xl space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('release.newTitle') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('release.newSubtitle') }}
      </p>
    </header>

    <Card>
      <CardHeader>
        <CardTitle>{{ t('release.newTitle') }}</CardTitle>
      </CardHeader>
      <CardContent>
        <form class="space-y-4" @submit="onCreate">
          <FormField name="tag_name">
            <FormItem>
              <FormLabel>{{ t('release.fields.tag') }}</FormLabel>
              <FormControl>
                <Select
                  :model-value="values.tag_name"
                  @update:model-value="(v) => setFieldValue('tag_name', String(v))"
                >
                  <SelectTrigger>
                    <SelectValue :placeholder="t('release.fields.tagHint')" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectLabel>{{ t('repo.tabs.tags') }}</SelectLabel>
                      <SelectItem v-for="tg in tags" :key="tg.name" :value="tg.name">
                        {{ tg.name }}
                      </SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </FormControl>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField v-slot="{ componentField }" name="title">
            <FormItem>
              <FormLabel>{{ t('release.fields.title') }}</FormLabel>
              <FormControl>
                <Input type="text" autocomplete="off" v-bind="componentField" />
              </FormControl>
              <p class="text-xs text-muted-foreground">{{ t('release.fields.titleHint') }}</p>
              <FormMessage />
            </FormItem>
          </FormField>

          <FormField v-slot="{ componentField }" name="notes">
            <FormItem>
              <FormLabel>{{ t('release.fields.notes') }}</FormLabel>
              <FormControl>
                <Textarea
                  rows="6"
                  :disabled="annotationLoading"
                  v-bind="componentField"
                  @input="userEdited = true"
                />
              </FormControl>
              <p v-if="showAutofillHint" class="flex items-center gap-1 text-xs text-muted-foreground">
                <Sparkles class="size-3" />
                {{ t('release.fields.notesAutofilled') }}
              </p>
              <p class="text-xs text-muted-foreground">{{ t('release.fields.notesHint') }}</p>
              <FormMessage />
            </FormItem>
          </FormField>

          <p v-if="createError" class="text-sm text-destructive">
            {{ createError }}
          </p>

          <div class="mt-2 flex items-center gap-3">
            <Button type="submit" :disabled="isSubmitting">
              <Rocket class="size-4" />
              {{ isSubmitting ? t('release.creating') : t('release.create') }}
            </Button>
            <Button type="button" variant="outline" @click="router.back()">
              {{ t('common.cancel') }}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
