<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toTypedSchema } from '@vee-validate/zod'
import * as z from 'zod'
import {
  AlertTriangle, Pencil, Plus, Power, PowerOff, Trash2, KeyRound,
  Layers, ChevronUp, ChevronDown, Circle, X, Eye,
} from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

import type {
  LLMProvider,
  LLMProviderCreateReq,
  LLMProviderListResp,
  LLMProviderPatchReq,
  ProviderType,
} from '~/types/llm-provider'
import type {
  LLMModelListResp,
} from '~/types/llm-model'
import type {
  ModelGroup,
  ModelGroupCreateReq,
  ModelGroupEntry,
  ModelGroupListItem,
  ModelGroupListResp,
  ModelGroupUpdateReq,
} from '~/types/model-group'

definePageMeta({ layout: 'admin' })

const { t } = useI18n()
useHead({ title: () => `${t('admin.llm.title')} - ${t('admin.section')} - ${t('app.name')}` })

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/llm' },
  { label: t('admin.llm.title') },
])

// ── Tab state ──────────────────────────────────────────────────────────
const activeTab = ref('providers')

// ── Providers state ────────────────────────────────────────────────────
const providers = ref<LLMProvider[]>([])
const providerLoading = ref(false)
const providerError = ref<string | null>(null)

const createOpen = ref(false)
const createError = ref<string | null>(null)

const editing = ref<LLMProvider | null>(null)
const editError = ref<string | null>(null)
const editOpen = computed({
  get: () => editing.value !== null,
  set: (v: boolean) => { if (!v) editing.value = null },
})

const PROVIDER_TYPES: ProviderType[] = ['openai', 'anthropic', 'openai-compat']

const createSchema = computed(() => toTypedSchema(z.object({
  name: z.string().min(1).max(64).regex(/^[a-z0-9][a-z0-9-]{0,63}$/),
  type: z.enum(['openai', 'anthropic', 'openai-compat']),
  base_url: z.string().url(),
  api_key: z.string().min(1),
})))

const editSchema = computed(() => toTypedSchema(z.object({
  base_url: z.string().url(),
  api_key: z.string().optional(),
})))

const createInitial = { name: '', type: 'openai-compat' as ProviderType, base_url: '', api_key: '' }
const editInitial = computed(() => editing.value ? {
  base_url: editing.value.base_url,
  api_key: '',
} : { base_url: '', api_key: '' })

async function loadProviders() {
  providerLoading.value = true
  providerError.value = null
  try {
    const res = await $fetch<LLMProviderListResp>('/api/admin/llm/providers', { credentials: 'include' })
    providers.value = res.items ?? []
  } catch (e: any) {
    providerError.value = e?.data?.error ?? t('admin.llm.loadFailed')
  } finally {
    providerLoading.value = false
  }
}

async function onCreate(values: any, ctx: any) {
  createError.value = null
  const body: LLMProviderCreateReq = {
    name: values.name,
    type: values.type,
    base_url: values.base_url,
    api_key: values.api_key,
  }
  try {
    await $fetch('/api/admin/llm/providers', { method: 'POST', credentials: 'include', body })
    createOpen.value = false
    ctx?.resetForm?.({ values: createInitial })
    await loadProviders()
  } catch (e: any) {
    createError.value = e?.data?.error ?? t('admin.llm.createFailed')
  }
}

async function onEdit(values: any) {
  if (!editing.value) return
  editError.value = null
  const body: LLMProviderPatchReq = {
    base_url: values.base_url,
  }
  if (values.api_key && values.api_key.trim()) {
    body.api_key = values.api_key
  }
  try {
    await $fetch(`/api/admin/llm/providers/${editing.value.name}`, {
      method: 'PATCH',
      credentials: 'include',
      body,
    })
    editing.value = null
    await loadProviders()
  } catch (e: any) {
    editError.value = e?.data?.error ?? t('admin.llm.updateFailed')
  }
}

async function onDelete(p: LLMProvider) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('admin.llm.deleteConfirm', { name: p.name }))) return
  try {
    await $fetch(`/api/admin/llm/providers/${p.name}`, { method: 'DELETE', credentials: 'include' })
    await loadProviders()
  } catch (e: any) {
    providerError.value = e?.data?.error ?? t('admin.llm.deleteFailed')
  }
}

async function onToggleDisabled(p: LLMProvider) {
  const willDisable = !p.disabled
  if (willDisable) {
    // eslint-disable-next-line no-alert
    if (!window.confirm(t('admin.llm.disableConfirm', { name: p.name }))) return
  }
  try {
    await $fetch(`/api/admin/llm/providers/${p.name}/disabled`, {
      method: 'POST',
      credentials: 'include',
      body: { disabled: willDisable },
    })
    await loadProviders()
  } catch (e: any) {
    providerError.value = e?.data?.error ?? t('admin.llm.toggleFailed')
  }
}

// ── Model groups state ─────────────────────────────────────────────────
const groups = ref<ModelGroupListItem[]>([])
const groupLoading = ref(false)
const groupError = ref<string | null>(null)

const groupCreateOpen = ref(false)
const groupCreateError = ref<string | null>(null)

const groupEditing = ref<ModelGroup | null>(null)
const groupEditError = ref<string | null>(null)
const groupEditOpen = computed({
  get: () => groupEditing.value !== null,
  set: (v: boolean) => { if (!v) groupEditing.value = null },
})

const groupDetail = ref<ModelGroup | null>(null)
const groupDetailOpen = computed({
  get: () => groupDetail.value !== null,
  set: (v: boolean) => { if (!v) groupDetail.value = null },
})
const groupDetailError = ref<string | null>(null)

// Available model names collected from all providers
const availableModels = ref<string[]>([])

const groupCreateSchema = computed(() => toTypedSchema(z.object({
  name: z.string().min(1).max(64).regex(/^[a-z0-9][a-z0-9-]{0,63}$/),
})))



// Editable entries for create/edit dialog
interface EditableEntry {
  key: number // local key for v-for
  model_name: string
  priority: number
}
let nextEntryKey = 0

const createEntries = ref<EditableEntry[]>([])
const editEntries = ref<EditableEntry[]>([])

function addCreateEntry() {
  createEntries.value.push({ key: nextEntryKey++, model_name: availableModels.value[0] ?? '', priority: createEntries.value.length })
}

function removeCreateEntry(idx: number) {
  createEntries.value.splice(idx, 1)
  createEntries.value.forEach((e, i) => { e.priority = i })
}

function moveCreateEntry(idx: number, dir: -1 | 1) {
  const target = idx + dir
  if (target < 0 || target >= createEntries.value.length) return
  const entries = createEntries.value
  const tmp = entries[idx]!
  entries[idx] = entries[target]!
  entries[target] = tmp
  entries.forEach((e, i) => { e.priority = i })
}

function addEditEntry() {
  editEntries.value.push({ key: nextEntryKey++, model_name: availableModels.value[0] ?? '', priority: editEntries.value.length })
}

function removeEditEntry(idx: number) {
  editEntries.value.splice(idx, 1)
  editEntries.value.forEach((e, i) => { e.priority = i })
}

function moveEditEntry(idx: number, dir: -1 | 1) {
  const target = idx + dir
  if (target < 0 || target >= editEntries.value.length) return
  const entries = editEntries.value
  const tmp = entries[idx]!
  entries[idx] = entries[target]!
  entries[target] = tmp
  entries.forEach((e, i) => { e.priority = i })
}

async function loadAvailableModels() {
  try {
    const res = await $fetch<LLMModelListResp>('/api/admin/llm/models', { credentials: 'include' })
    availableModels.value = (res.items ?? []).map(m => m.name).sort()
  } catch {
    // non-fatal
  }
}

async function loadGroups() {
  groupLoading.value = true
  groupError.value = null
  try {
    const res = await $fetch<ModelGroupListResp>('/api/admin/model-groups', { credentials: 'include' })
    groups.value = res.items ?? []
  } catch (e: any) {
    groupError.value = e?.data?.error ?? t('admin.llm.modelGroups.loadFailed')
  } finally {
    groupLoading.value = false
  }
}

async function onGroupCreate(values: any, ctx: any) {
  groupCreateError.value = null
  const body: ModelGroupCreateReq = {
    name: values.name,
    entries: createEntries.value.map(e => ({ model_name: e.model_name, priority: e.priority })),
  }
  try {
    await $fetch('/api/admin/model-groups', { method: 'POST', credentials: 'include', body })
    groupCreateOpen.value = false
    createEntries.value = []
    ctx?.resetForm?.({ values: { name: '' } })
    await loadGroups()
  } catch (e: any) {
    groupCreateError.value = e?.data?.error ?? t('admin.llm.modelGroups.createFailed')
  }
}

async function fetchGroupDetail(name: string): Promise<ModelGroup | null> {
  try {
    return await $fetch<ModelGroup>(`/api/admin/model-groups/${encodeURIComponent(name)}`, { credentials: 'include' })
  } catch {
    return null
  }
}

async function openGroupDetail(g: ModelGroupListItem) {
  groupDetailError.value = null
  const res = await fetchGroupDetail(g.name)
  if (res) {
    groupDetail.value = res
  } else {
    groupDetailError.value = t('admin.llm.modelGroups.loadFailed')
  }
}

async function openGroupEdit(g: ModelGroupListItem) {
  groupEditError.value = null
  const res = await fetchGroupDetail(g.name)
  if (res) {
    groupEditing.value = res
    editEntries.value = res.entries.map(e => ({ key: nextEntryKey++, model_name: e.model_name, priority: e.priority }))
  } else {
    groupEditError.value = t('admin.llm.modelGroups.loadFailed')
  }
}

async function onGroupEdit(values: any) {
  if (!groupEditing.value) return
  groupEditError.value = null
  const body: ModelGroupUpdateReq = {
    entries: editEntries.value.map(e => ({ model_name: e.model_name, priority: e.priority })),
  }
  try {
    await $fetch(`/api/admin/model-groups/${encodeURIComponent(groupEditing.value.name)}`, {
      method: 'PATCH',
      credentials: 'include',
      body,
    })
    groupEditing.value = null
    editEntries.value = []
    await loadGroups()
  } catch (e: any) {
    groupEditError.value = e?.data?.error ?? t('admin.llm.modelGroups.updateFailed')
  }
}

async function onGroupDelete(g: ModelGroupListItem) {
  // eslint-disable-next-line no-alert
  if (!window.confirm(t('admin.llm.modelGroups.deleteConfirm', { name: g.name }))) return
  try {
    await $fetch(`/api/admin/model-groups/${encodeURIComponent(g.name)}`, { method: 'DELETE', credentials: 'include' })
    await loadGroups()
  } catch (e: any) {
    groupError.value = e?.data?.error ?? t('admin.llm.modelGroups.deleteFailed')
  }
}

async function onToggleEntry(groupName: string, entry: ModelGroupEntry) {
  const willDisable = entry.status !== 'manual_disabled'
  if (willDisable) {
    // eslint-disable-next-line no-alert
    if (!window.confirm(t('admin.llm.modelGroups.actions.disableConfirm', { model: entry.model_name }))) return
  }
  const action = willDisable ? 'disable' : 'enable'
  try {
    await $fetch(`/api/admin/model-groups/${encodeURIComponent(groupName)}/entries/${entry.id}/${action}`, {
      method: 'POST',
      credentials: 'include',
    })
    // Refresh detail and list
    const fresh = await fetchGroupDetail(groupName)
    if (fresh) groupDetail.value = fresh
    await loadGroups()
  } catch (e: any) {
    groupDetailError.value = e?.data?.error ?? t('admin.llm.modelGroups.actions.toggleFailed')
  }
}

function statusBadgeVariant(status: string) {
  if (status === 'available') return 'secondary'
  if (status === 'auto_disabled') return 'default'
  return 'destructive'
}

function formatRemaining(seconds: number | undefined): string {
  if (!seconds || seconds <= 0) return ''
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.ceil(seconds / 60)}m`
  if (seconds < 86400) return `${Math.ceil(seconds / 3600)}h`
  return `${Math.ceil(seconds / 86400)}d`
}

onMounted(async () => {
  await loadProviders()
  await loadAvailableModels()
  await loadGroups()
})
</script>

<template>
  <div class="space-y-6">
    <header class="space-y-1">
      <h1 class="text-2xl font-semibold tracking-tight">
        {{ t('admin.llm.title') }}
      </h1>
      <p class="text-sm text-muted-foreground">
        {{ t('admin.llm.subtitle') }}
      </p>
    </header>

    <Tabs v-model="activeTab">
      <TabsList>
        <TabsTrigger value="providers">
          {{ t('admin.llm.modelGroups.tabs.providers') }}
        </TabsTrigger>
        <TabsTrigger value="groups">
          {{ t('admin.llm.modelGroups.tabs.groups') }}
        </TabsTrigger>
      </TabsList>

      <!-- ═══ Providers tab ═══ -->
      <TabsContent value="providers" class="mt-4 space-y-6">
        <div class="flex items-start justify-between gap-4">
          <Button @click="createOpen = true">
            <Plus class="size-4" />
            {{ t('admin.llm.create') }}
          </Button>
        </div>

        <Card>
          <CardHeader>
            <CardTitle>{{ t('admin.llm.cardTitle') }}</CardTitle>
            <CardDescription>{{ t('admin.llm.cardDescription') }}</CardDescription>
          </CardHeader>
          <CardContent>
            <p v-if="providerError" class="mb-3 text-sm text-destructive">{{ providerError }}</p>

            <div v-if="!providerLoading && providers.length === 0" class="rounded-lg border border-dashed p-8 text-center">
              <KeyRound class="mx-auto size-8 text-muted-foreground" />
              <p class="mt-3 text-sm font-medium">{{ t('admin.llm.empty') }}</p>
              <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.llm.emptyHint') }}</p>
            </div>

            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>{{ t('admin.llm.cols.name') }}</TableHead>
                  <TableHead>{{ t('admin.llm.cols.type') }}</TableHead>
                  <TableHead>{{ t('admin.llm.cols.status') }}</TableHead>
                  <TableHead>{{ t('admin.llm.cols.baseUrl') }}</TableHead>
                  <TableHead>{{ t('admin.llm.cols.apiKey') }}</TableHead>
                  <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="p in providers" :key="p.id" :class="p.disabled ? 'opacity-60' : ''">
                  <TableCell class="font-medium">{{ p.name }}</TableCell>
                  <TableCell><Badge variant="outline">{{ p.type }}</Badge></TableCell>
                  <TableCell>
                    <Badge v-if="p.disabled" variant="destructive">{{ t('admin.llm.disabled') }}</Badge>
                    <Badge v-else variant="secondary">{{ t('admin.llm.enabled') }}</Badge>
                  </TableCell>
                  <TableCell class="font-mono text-xs text-muted-foreground">{{ p.base_url }}</TableCell>
                  <TableCell>
                    <Badge v-if="p.has_api_key" variant="secondary">{{ t('admin.llm.apiKeySet') }}</Badge>
                    <Badge v-else variant="destructive">{{ t('admin.llm.apiKeyMissing') }}</Badge>
                  </TableCell>
                  <TableCell class="space-x-2 text-right">
                    <Button size="sm" variant="outline" @click="onToggleDisabled(p)">
                      <Power v-if="p.disabled" class="size-3" />
                      <PowerOff v-else class="size-3" />
                      {{ p.disabled ? t('admin.llm.enable') : t('admin.llm.disable') }}
                    </Button>
                    <Button size="sm" variant="outline" @click="editing = p">
                      <Pencil class="size-3" />
                      {{ t('common.edit') }}
                    </Button>
                    <Button size="sm" variant="destructive" @click="onDelete(p)">
                      <Trash2 class="size-3" />
                      {{ t('common.delete') }}
                    </Button>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>

            <p v-if="providerLoading" class="mt-3 text-sm text-muted-foreground">{{ t('common.loading') }}</p>
          </CardContent>
        </Card>

        <!-- Create provider dialog -->
        <Dialog v-model:open="createOpen">
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{{ t('admin.llm.createTitle') }}</DialogTitle>
              <DialogDescription>{{ t('admin.llm.createSubtitle') }}</DialogDescription>
            </DialogHeader>
            <Form v-slot="{ isSubmitting, values, setFieldValue }" :validation-schema="createSchema" :initial-values="createInitial" keep-values @submit="onCreate">
              <div class="space-y-4">
                <FormField v-slot="{ componentField }" name="name">
                  <FormItem>
                    <FormLabel>{{ t('admin.llm.fields.name') }}</FormLabel>
                    <FormControl><Input v-bind="componentField" autocomplete="off" /></FormControl>
                    <p class="text-xs text-muted-foreground">{{ t('admin.llm.fields.nameHint') }}</p>
                    <FormMessage />
                  </FormItem>
                </FormField>

                <FormField name="type">
                  <FormItem>
                    <FormLabel>{{ t('admin.llm.fields.type') }}</FormLabel>
                    <FormControl>
                      <Select :model-value="values.type" @update:model-value="(v) => setFieldValue('type', v as ProviderType)">
                        <SelectTrigger><SelectValue /></SelectTrigger>
                        <SelectContent>
                          <SelectItem v-for="ty in PROVIDER_TYPES" :key="ty" :value="ty">{{ ty }}</SelectItem>
                        </SelectContent>
                      </Select>
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                </FormField>

                <FormField v-slot="{ componentField }" name="base_url">
                  <FormItem>
                    <FormLabel>{{ t('admin.llm.fields.baseUrl') }}</FormLabel>
                    <FormControl><Input v-bind="componentField" placeholder="https://api.example.com/v1" /></FormControl>
                    <FormMessage />
                  </FormItem>
                </FormField>

                <FormField v-slot="{ componentField }" name="api_key">
                  <FormItem>
                    <FormLabel>{{ t('admin.llm.fields.apiKey') }}</FormLabel>
                    <FormControl><Input type="password" autocomplete="off" v-bind="componentField" /></FormControl>
                    <p class="text-xs text-muted-foreground">{{ t('admin.llm.fields.apiKeyHint') }}</p>
                    <FormMessage />
                  </FormItem>
                </FormField>

                <p v-if="createError" class="text-sm text-destructive">{{ createError }}</p>
              </div>
              <DialogFooter class="mt-6">
                <Button type="button" variant="outline" @click="createOpen = false">{{ t('common.cancel') }}</Button>
                <Button type="submit" :disabled="isSubmitting">{{ isSubmitting ? t('common.submitting') : t('common.submit') }}</Button>
              </DialogFooter>
            </Form>
          </DialogContent>
        </Dialog>

        <!-- Edit provider dialog -->
        <Dialog v-model:open="editOpen">
          <DialogContent>
            <DialogHeader>
              <DialogTitle>{{ t('admin.llm.editTitle', { name: editing?.name }) }}</DialogTitle>
              <DialogDescription>{{ t('admin.llm.editSubtitle') }}</DialogDescription>
            </DialogHeader>
            <Form v-if="editing" v-slot="{ isSubmitting }" :validation-schema="editSchema" :initial-values="editInitial" keep-values @submit="onEdit">
              <div class="space-y-4">
                <FormField v-slot="{ componentField }" name="base_url">
                  <FormItem>
                    <FormLabel>{{ t('admin.llm.fields.baseUrl') }}</FormLabel>
                    <FormControl><Input v-bind="componentField" /></FormControl>
                    <FormMessage />
                  </FormItem>
                </FormField>

                <FormField v-slot="{ componentField }" name="api_key">
                  <FormItem>
                    <FormLabel>{{ t('admin.llm.fields.apiKey') }}</FormLabel>
                    <FormControl><Input type="password" autocomplete="off" v-bind="componentField" /></FormControl>
                    <p class="text-xs text-muted-foreground flex items-start gap-1">
                      <AlertTriangle class="mt-0.5 size-3 shrink-0 text-amber-500" />
                      {{ t('admin.llm.fields.apiKeyEditHint') }}
                    </p>
                    <FormMessage />
                  </FormItem>
                </FormField>

                <p v-if="editError" class="text-sm text-destructive">{{ editError }}</p>
              </div>
              <DialogFooter class="mt-6">
                <Button type="button" variant="outline" @click="editing = null">{{ t('common.cancel') }}</Button>
                <Button type="submit" :disabled="isSubmitting">{{ isSubmitting ? t('common.submitting') : t('common.save') }}</Button>
              </DialogFooter>
            </Form>
          </DialogContent>
        </Dialog>
      </TabsContent>

      <!-- ═══ Model groups tab ═══ -->
      <TabsContent value="groups" class="mt-4 space-y-6">
        <div class="flex items-start justify-between gap-4">
          <Button @click="groupCreateOpen = true; createEntries = [];">
            <Plus class="size-4" />
            {{ t('admin.llm.modelGroups.create') }}
          </Button>
        </div>

        <Card>
          <CardHeader>
            <CardTitle>{{ t('admin.llm.modelGroups.listTitle') }}</CardTitle>
            <CardDescription>{{ t('admin.llm.modelGroups.listDescription') }}</CardDescription>
          </CardHeader>
          <CardContent>
            <p v-if="groupError" class="mb-3 text-sm text-destructive">{{ groupError }}</p>

            <div v-if="!groupLoading && groups.length === 0" class="rounded-lg border border-dashed p-8 text-center">
              <Layers class="mx-auto size-8 text-muted-foreground" />
              <p class="mt-3 text-sm font-medium">{{ t('admin.llm.modelGroups.empty') }}</p>
              <p class="mt-1 text-xs text-muted-foreground">{{ t('admin.llm.modelGroups.emptyHint') }}</p>
            </div>

            <Table v-else>
              <TableHeader>
                <TableRow>
                  <TableHead>{{ t('admin.llm.modelGroups.cols.name') }}</TableHead>
                  <TableHead>{{ t('admin.llm.modelGroups.cols.models') }}</TableHead>
                  <TableHead>{{ t('admin.llm.modelGroups.cols.available') }}</TableHead>
                  <TableHead>{{ t('admin.llm.modelGroups.cols.created') }}</TableHead>
                  <TableHead class="text-right">{{ t('common.actions') }}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="g in groups" :key="g.id">
                  <TableCell class="font-medium">
                    <div class="flex items-center gap-2">
                      {{ g.name }}
                      <AlertTriangle v-if="g.available_count === 0 && g.entry_count > 0" class="size-3.5 text-amber-500" />
                    </div>
                  </TableCell>
                  <TableCell>{{ g.entry_count }}</TableCell>
                  <TableCell>
                    <Badge :variant="g.available_count > 0 ? 'secondary' : 'destructive'">
                      {{ g.available_count }} / {{ g.entry_count }}
                    </Badge>
                  </TableCell>
                  <TableCell class="text-xs text-muted-foreground">{{ new Date(g.created_at).toLocaleString() }}</TableCell>
                  <TableCell class="space-x-2 text-right">
                    <Button size="sm" variant="outline" @click="openGroupDetail(g)">
                      <Eye class="size-3" />
                      {{ t('admin.llm.modelGroups.actions.detail') }}
                    </Button>
                    <Button size="sm" variant="outline" @click="openGroupEdit(g)">
                      <Pencil class="size-3" />
                      {{ t('common.edit') }}
                    </Button>
                    <Button size="sm" variant="destructive" @click="onGroupDelete(g)">
                      <Trash2 class="size-3" />
                      {{ t('common.delete') }}
                    </Button>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>

            <p v-if="groupLoading" class="mt-3 text-sm text-muted-foreground">{{ t('common.loading') }}</p>
          </CardContent>
        </Card>

        <!-- Create group dialog -->
        <Dialog v-model:open="groupCreateOpen">
          <DialogContent class="max-w-lg">
            <DialogHeader>
              <DialogTitle>{{ t('admin.llm.modelGroups.createTitle') }}</DialogTitle>
              <DialogDescription>{{ t('admin.llm.modelGroups.createSubtitle') }}</DialogDescription>
            </DialogHeader>
            <Form v-slot="{ isSubmitting }" :validation-schema="groupCreateSchema" :initial-values="{ name: '' }" keep-values @submit="onGroupCreate">
              <div class="space-y-4">
                <FormField v-slot="{ componentField }" name="name">
                  <FormItem>
                    <FormLabel>{{ t('admin.llm.modelGroups.fields.name') }}</FormLabel>
                    <FormControl><Input v-bind="componentField" autocomplete="off" /></FormControl>
                    <p class="text-xs text-muted-foreground">{{ t('admin.llm.modelGroups.fields.nameHint') }}</p>
                    <FormMessage />
                  </FormItem>
                </FormField>

                <!-- Entry list -->
                <div class="space-y-2">
                  <div class="flex items-center justify-between">
                    <span class="text-sm font-medium">{{ t('admin.llm.modelGroups.fields.entries') }}</span>
                    <Button type="button" size="sm" variant="outline" @click="addCreateEntry">
                      <Plus class="size-3" />
                      {{ t('admin.llm.modelGroups.fields.addModel') }}
                    </Button>
                  </div>
                  <p class="text-xs text-muted-foreground">{{ t('admin.llm.modelGroups.fields.entriesHint') }}</p>

                  <div v-if="createEntries.length === 0" class="rounded-md border border-dashed p-4 text-center text-xs text-muted-foreground">
                    {{ t('admin.llm.modelGroups.fields.entriesHint') }}
                  </div>

                  <div v-for="(entry, idx) in createEntries" :key="entry.key" class="flex items-center gap-2 rounded-md border p-2">
                    <span class="w-6 text-center text-xs font-mono text-muted-foreground">{{ idx + 1 }}</span>
                    <Select v-model="entry.model_name" class="flex-1">
                      <SelectTrigger><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem v-for="m in availableModels" :key="m" :value="m">{{ m }}</SelectItem>
                      </SelectContent>
                    </Select>
                    <div class="flex gap-0.5">
                      <Button type="button" size="icon" variant="ghost" class="size-7" :disabled="idx === 0" @click="moveCreateEntry(idx, -1)">
                        <ChevronUp class="size-3.5" />
                      </Button>
                      <Button type="button" size="icon" variant="ghost" class="size-7" :disabled="idx === createEntries.length - 1" @click="moveCreateEntry(idx, 1)">
                        <ChevronDown class="size-3.5" />
                      </Button>
                    </div>
                    <Button type="button" size="icon" variant="ghost" class="size-7 text-destructive" @click="removeCreateEntry(idx)">
                      <X class="size-3.5" />
                    </Button>
                  </div>
                </div>

                <p v-if="groupCreateError" class="text-sm text-destructive">{{ groupCreateError }}</p>
              </div>
              <DialogFooter class="mt-6">
                <Button type="button" variant="outline" @click="groupCreateOpen = false">{{ t('common.cancel') }}</Button>
                <Button type="submit" :disabled="isSubmitting">{{ isSubmitting ? t('common.submitting') : t('common.submit') }}</Button>
              </DialogFooter>
            </Form>
          </DialogContent>
        </Dialog>

        <!-- Edit group dialog -->
        <Dialog v-model:open="groupEditOpen">
          <DialogContent class="max-w-lg">
            <DialogHeader>
              <DialogTitle>{{ t('admin.llm.modelGroups.editTitle', { name: groupEditing?.name }) }}</DialogTitle>
              <DialogDescription>{{ t('admin.llm.modelGroups.editSubtitle') }}</DialogDescription>
            </DialogHeader>
            <Form v-if="groupEditing" v-slot="{ isSubmitting }" @submit="onGroupEdit">
              <div class="space-y-4">
                <div>
                  <span class="text-sm font-medium text-muted-foreground">{{ t('admin.llm.modelGroups.fields.name') }}</span>
                  <p class="text-sm font-mono">{{ groupEditing.name }}</p>
                </div>

                <!-- Entry list -->
                <div class="space-y-2">
                  <div class="flex items-center justify-between">
                    <span class="text-sm font-medium">{{ t('admin.llm.modelGroups.fields.entries') }}</span>
                    <Button type="button" size="sm" variant="outline" @click="addEditEntry">
                      <Plus class="size-3" />
                      {{ t('admin.llm.modelGroups.fields.addModel') }}
                    </Button>
                  </div>
                  <p class="text-xs text-muted-foreground">{{ t('admin.llm.modelGroups.fields.entriesHint') }}</p>

                  <div v-if="editEntries.length === 0" class="rounded-md border border-dashed p-4 text-center text-xs text-muted-foreground">
                    {{ t('admin.llm.modelGroups.fields.entriesHint') }}
                  </div>

                  <div v-for="(entry, idx) in editEntries" :key="entry.key" class="flex items-center gap-2 rounded-md border p-2">
                    <span class="w-6 text-center text-xs font-mono text-muted-foreground">{{ idx + 1 }}</span>
                    <Select v-model="entry.model_name" class="flex-1">
                      <SelectTrigger><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem v-for="m in availableModels" :key="m" :value="m">{{ m }}</SelectItem>
                      </SelectContent>
                    </Select>
                    <div class="flex gap-0.5">
                      <Button type="button" size="icon" variant="ghost" class="size-7" :disabled="idx === 0" @click="moveEditEntry(idx, -1)">
                        <ChevronUp class="size-3.5" />
                      </Button>
                      <Button type="button" size="icon" variant="ghost" class="size-7" :disabled="idx === editEntries.length - 1" @click="moveEditEntry(idx, 1)">
                        <ChevronDown class="size-3.5" />
                      </Button>
                    </div>
                    <Button type="button" size="icon" variant="ghost" class="size-7 text-destructive" @click="removeEditEntry(idx)">
                      <X class="size-3.5" />
                    </Button>
                  </div>
                </div>

                <p v-if="groupEditError" class="text-sm text-destructive">{{ groupEditError }}</p>
              </div>
              <DialogFooter class="mt-6">
                <Button type="button" variant="outline" @click="groupEditing = null; editEntries = []">{{ t('common.cancel') }}</Button>
                <Button type="submit" :disabled="isSubmitting">{{ isSubmitting ? t('common.submitting') : t('common.save') }}</Button>
              </DialogFooter>
            </Form>
          </DialogContent>
        </Dialog>

        <!-- Detail dialog -->
        <Dialog v-model:open="groupDetailOpen">
          <DialogContent class="max-w-xl">
            <DialogHeader>
              <DialogTitle>{{ t('admin.llm.modelGroups.detailTitle', { name: groupDetail?.name }) }}</DialogTitle>
              <DialogDescription>{{ t('admin.llm.modelGroups.detailSubtitle') }}</DialogDescription>
            </DialogHeader>

            <p v-if="groupDetailError" class="text-sm text-destructive">{{ groupDetailError }}</p>

            <div v-if="groupDetail" class="space-y-3">
              <div
                v-for="entry in groupDetail.entries"
                :key="entry.id"
                class="flex items-center justify-between rounded-lg border p-3"
              >
                <div class="space-y-1 min-w-0">
                  <div class="flex items-center gap-2">
                    <span class="font-mono text-sm font-medium truncate">{{ entry.model_name }}</span>
                    <span class="text-xs text-muted-foreground">#{{ entry.priority + 1 }}</span>
                  </div>
                  <div class="flex items-center gap-2 text-xs text-muted-foreground">
                    <Badge :variant="statusBadgeVariant(entry.status)" class="text-xs">
                      {{ entry.status === 'available' ? t('admin.llm.modelGroups.status.available')
                        : entry.status === 'auto_disabled' ? t('admin.llm.modelGroups.status.autoDisabled')
                        : t('admin.llm.modelGroups.status.manualDisabled') }}
                    </Badge>
                    <span v-if="entry.status === 'auto_disabled' && entry.remaining_seconds" class="text-amber-500">
                      {{ t('admin.llm.modelGroups.status.autoDisabledHint', { remaining: formatRemaining(entry.remaining_seconds) }) }}
                    </span>
                    <span v-if="entry.last_checked_at">
                      {{ new Date(entry.last_checked_at).toLocaleString() }}
                    </span>
                  </div>
                </div>
                <div class="shrink-0 ml-3">
                  <Button
                    v-if="entry.status !== 'manual_disabled'"
                    size="sm"
                    variant="destructive"
                    @click="onToggleEntry(groupDetail.name, entry)"
                  >
                    {{ t('admin.llm.modelGroups.actions.disable') }}
                  </Button>
                  <Button
                    v-else
                    size="sm"
                    variant="outline"
                    @click="onToggleEntry(groupDetail.name, entry)"
                  >
                    <Power class="size-3" />
                    {{ t('admin.llm.modelGroups.actions.enable') }}
                  </Button>
                </div>
              </div>

              <div v-if="groupDetail.entries.length === 0" class="rounded-md border border-dashed p-4 text-center text-xs text-muted-foreground">
                {{ t('admin.llm.modelGroups.emptyHint') }}
              </div>
            </div>

            <DialogFooter>
              <Button variant="outline" @click="groupDetail = null">{{ t('common.close') }}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </TabsContent>
    </Tabs>
  </div>
</template>
