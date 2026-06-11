<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Pencil, Plus, Trash2 } from 'lucide-vue-next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'

import type {
  LLMModel,
  LLMModelCreateMember,
  LLMModelCreateReq,
  LLMModelListItem,
  LLMModelListResp,
  LLMModelPatchReq,
} from '~/types/llm-model'
import type { LLMProvider } from '~/types/llm-provider'

definePageMeta({ layout: 'admin' })

const { t } = useI18n()
useHead({ title: () => `${t('admin.llmModels.title')} - ${t('admin.section')} - ${t('app.name')}` })

setBreadcrumbs(() => [
  { label: t('admin.section'), to: '/admin/llm-models' },
  { label: t('admin.llmModels.title') },
])

const items = ref<LLMModelListItem[]>([])
const loading = ref(false)
const error = ref('')

const providers = ref<LLMProvider[]>([])

const dialogOpen = ref(false)
const editing = ref<LLMModel | null>(null)
const saving = ref(false)
const saveError = ref('')

interface EffortEntry {
  key: string
  value: string
}
const effortEntries = ref<EffortEntry[]>([])

interface MemberEntry {
  provider_id: number
  model: string
}
const memberEntries = ref<MemberEntry[]>([])

const form = ref({
  name: '',
  display_name: '',
  context_window: 200000,
  max_output_tokens: 32000,
  vision: false,
})

const deleteTarget = ref<LLMModelListItem | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)

async function loadItems() {
  loading.value = true
  error.value = ''
  try {
    const resp = await $fetch<LLMModelListResp>('/api/admin/llm/models')
    items.value = resp.items ?? []
  } catch (e: any) {
    error.value = e?.data?.error || e?.message || t('admin.llmModels.loadFailed')
  } finally {
    loading.value = false
  }
}

async function loadProviders() {
  try {
    const resp = await $fetch<{ items: LLMProvider[] }>('/api/admin/llm/providers')
    providers.value = (resp.items ?? []).filter(p => !p.disabled)
  } catch {
    // providers are optional for the form
  }
}

function openCreate() {
  editing.value = null
  form.value = {
    name: '',
    display_name: '',
    context_window: 200000,
    max_output_tokens: 32000,
    vision: false,
  }
  effortEntries.value = []
  memberEntries.value = []
  saveError.value = ''
  dialogOpen.value = true
}

async function openEdit(item: LLMModelListItem) {
  try {
    const model = await $fetch<LLMModel>(`/api/admin/llm/models/${item.name}`)
    editing.value = model
    form.value = {
      name: model.name,
      display_name: model.display_name,
      context_window: model.context_window,
      max_output_tokens: model.max_output_tokens,
      vision: model.vision,
    }
    effortEntries.value = Object.entries(model.reasoning_effort_map ?? {}).map(([key, value]) => ({ key, value }))
    memberEntries.value = (model.members ?? []).map(member => ({ provider_id: member.provider_id, model: member.model }))
    saveError.value = ''
    dialogOpen.value = true
  } catch {
    // silently fail
  }
}

function closeDialog() {
  dialogOpen.value = false
  editing.value = null
}

function addEffortEntry() {
  effortEntries.value.push({ key: '', value: '' })
}

function removeEffortEntry(index: number) {
  effortEntries.value.splice(index, 1)
}

function quickInsertEffort() {
  const standard = ['minimal', 'low', 'medium', 'high', 'max']
  const existing = new Set(effortEntries.value.map(entry => entry.key))
  for (const key of standard) {
    if (!existing.has(key)) {
      effortEntries.value.push({ key, value: '' })
    }
  }
}

function addMemberEntry() {
  memberEntries.value.push({ provider_id: providers.value[0]?.id ?? 0, model: '' })
}

function removeMemberEntry(index: number) {
  memberEntries.value.splice(index, 1)
}

function buildEffortMap(): Record<string, string> {
  const map: Record<string, string> = {}
  for (const entry of effortEntries.value) {
    const key = entry.key.trim()
    if (key) map[key] = entry.value
  }
  return map
}

function buildMembers(): LLMModelCreateMember[] {
  return memberEntries.value
    .filter(member => member.provider_id > 0 && member.model.trim())
    .map(member => ({ provider_id: member.provider_id, model: member.model.trim() }))
}

async function save() {
  saving.value = true
  saveError.value = ''
  try {
    const effortMap = buildEffortMap()
    const members = buildMembers()

    if (editing.value) {
      const patch: LLMModelPatchReq = {
        display_name: form.value.display_name,
        context_window: form.value.context_window,
        max_output_tokens: form.value.max_output_tokens,
        vision: form.value.vision,
        reasoning_effort_map: effortMap,
        members,
      }
      await $fetch(`/api/admin/llm/models/${editing.value.name}`, {
        method: 'PATCH',
        body: patch,
      })
    } else {
      const create: LLMModelCreateReq = {
        name: form.value.name,
        display_name: form.value.display_name,
        context_window: form.value.context_window,
        max_output_tokens: form.value.max_output_tokens,
        vision: form.value.vision,
        reasoning_effort_map: effortMap,
        members,
      }
      await $fetch('/api/admin/llm/models', {
        method: 'POST',
        body: create,
      })
    }
    dialogOpen.value = false
    await loadItems()
  } catch (e: any) {
    saveError.value = e?.data?.error || e?.message || (editing.value ? t('admin.llmModels.updateFailed') : t('admin.llmModels.createFailed'))
  } finally {
    saving.value = false
  }
}

function confirmDelete(item: LLMModelListItem) {
  deleteTarget.value = item
  deleteOpen.value = true
}

async function deleteModel() {
  if (!deleteTarget.value) return
  deleting.value = true
  try {
    await $fetch(`/api/admin/llm/models/${deleteTarget.value.name}`, { method: 'DELETE' })
    deleteOpen.value = false
    await loadItems()
  } catch {
    // ignore
  } finally {
    deleting.value = false
  }
}

onMounted(async () => {
  await Promise.all([loadItems(), loadProviders()])
})
</script>

<template>
  <div class="space-y-6">
    <Card>
      <CardHeader>
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>{{ t('admin.llmModels.title') }}</CardTitle>
            <CardDescription>{{ t('admin.llmModels.subtitle') }}</CardDescription>
          </div>
          <Button class="w-full sm:w-auto" @click="openCreate">
            <Plus class="mr-1 h-4 w-4" />
            {{ t('admin.llmModels.create') }}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        <div v-if="error" class="mb-4 text-sm text-red-600">{{ error }}</div>
        <div v-if="loading" class="py-8 text-center text-muted-foreground">{{ t('common.loading') }}</div>
        <div v-else-if="items.length === 0" class="py-8 text-center text-muted-foreground">
          <p>{{ t('admin.llmModels.empty') }}</p>
          <p class="text-sm">{{ t('admin.llmModels.emptyHint') }}</p>
        </div>
        <div v-else class="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{{ t('admin.llmModels.cols.name') }}</TableHead>
                <TableHead>{{ t('admin.llmModels.cols.contextWindow') }}</TableHead>
                <TableHead>{{ t('admin.llmModels.cols.maxOutput') }}</TableHead>
                <TableHead>{{ t('admin.llmModels.cols.vision') }}</TableHead>
                <TableHead>{{ t('admin.llmModels.cols.providers') }}</TableHead>
                <TableHead>{{ t('admin.llmModels.cols.health') }}</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="item in items" :key="item.id">
                <TableCell class="font-medium">
                  <div>{{ item.display_name || item.name }}</div>
                  <div v-if="item.display_name" class="text-xs text-muted-foreground">{{ item.name }}</div>
                </TableCell>
                <TableCell>{{ item.context_window.toLocaleString() }}</TableCell>
                <TableCell>{{ item.max_output_tokens.toLocaleString() }}</TableCell>
                <TableCell>
                  <Badge :variant="item.vision ? 'default' : 'outline'">
                    {{ item.vision ? '✓' : '—' }}
                  </Badge>
                </TableCell>
                <TableCell>{{ item.member_count }}</TableCell>
                <TableCell>
                  <div class="flex items-center gap-1">
                    <span
                      class="inline-block h-2 w-2 rounded-full"
                      :class="item.available_count === item.member_count && item.member_count > 0 ? 'bg-green-500' : item.available_count > 0 ? 'bg-yellow-500' : 'bg-red-500'"
                    />
                    <span class="text-xs">{{ item.available_count }}/{{ item.member_count }}</span>
                  </div>
                </TableCell>
                <TableCell>
                  <div class="flex items-center gap-1">
                    <Button size="icon" variant="ghost" @click="openEdit(item)">
                      <Pencil class="h-4 w-4" />
                    </Button>
                    <Button size="icon" variant="ghost" @click="confirmDelete(item)">
                      <Trash2 class="h-4 w-4 text-red-500" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
      </CardContent>
    </Card>

    <Dialog :open="dialogOpen" @update:open="closeDialog">
      <DialogContent class="max-h-[85vh] w-[calc(100vw-2rem)] max-w-2xl overflow-x-hidden overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {{ editing ? t('admin.llmModels.editTitle', { name: editing.name }) : t('admin.llmModels.createTitle') }}
          </DialogTitle>
          <DialogDescription>
            {{ editing ? t('admin.llmModels.editSubtitle') : t('admin.llmModels.createSubtitle') }}
          </DialogDescription>
        </DialogHeader>

        <div v-if="saveError" class="mb-2 text-sm text-red-600">{{ saveError }}</div>

        <div class="min-w-0 space-y-4">
          <div v-if="!editing" class="space-y-1">
            <Label>{{ t('admin.llmModels.fields.name') }}</Label>
            <Input v-model="form.name" :placeholder="t('admin.llmModels.fields.nameHint')" />
          </div>

          <div class="space-y-1">
            <Label>{{ t('admin.llmModels.fields.displayName') }}</Label>
            <Input v-model="form.display_name" :placeholder="t('admin.llmModels.fields.displayNameHint')" />
          </div>

          <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div class="space-y-1">
              <Label>{{ t('admin.llmModels.fields.contextWindow') }}</Label>
              <Input v-model.number="form.context_window" type="number" min="1" />
            </div>
            <div class="space-y-1">
              <Label>{{ t('admin.llmModels.fields.maxOutputTokens') }}</Label>
              <Input v-model.number="form.max_output_tokens" type="number" min="1" />
            </div>
          </div>

          <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:gap-6">
            <div class="flex items-center gap-2">
              <Switch v-model:checked="form.vision" />
              <Label>{{ t('admin.llmModels.fields.vision') }}</Label>
            </div>
          </div>

          <div class="space-y-2">
            <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <Label>{{ t('admin.llmModels.effortMap') }}</Label>
              <div class="flex flex-wrap items-center gap-1">
                <Button size="sm" variant="outline" @click="quickInsertEffort">
                  {{ t('admin.llmModels.effortMapHint') }} {{ t('admin.llmModels.effortMapLevels') }}
                </Button>
                <Button size="sm" variant="ghost" @click="addEffortEntry">
                  <Plus class="h-3 w-3" /> {{ t('admin.llmModels.effortMapAdd') }}
                </Button>
              </div>
            </div>
            <div
              v-for="(entry, i) in effortEntries"
              :key="i"
              class="grid grid-cols-1 gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] sm:items-center"
            >
              <Input v-model="entry.key" :placeholder="t('admin.llmModels.effortMapKey')" class="min-w-0" />
              <Input v-model="entry.value" :placeholder="t('admin.llmModels.effortMapValue')" class="min-w-0" />
              <Button size="icon" variant="ghost" class="shrink-0" @click="removeEffortEntry(i)">
                <Trash2 class="h-4 w-4" />
              </Button>
            </div>
          </div>

          <div class="space-y-2">
            <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
              <Label>{{ t('admin.llmModels.members') }}</Label>
              <Button size="sm" variant="ghost" @click="addMemberEntry">
                <Plus class="h-3 w-3" /> {{ t('admin.llmModels.memberAdd') }}
              </Button>
            </div>
            <div
              v-for="(entry, i) in memberEntries"
              :key="i"
              class="grid grid-cols-1 gap-2 sm:grid-cols-[minmax(0,180px)_minmax(0,1fr)_auto] sm:items-center"
            >
              <Select v-model="entry.provider_id">
                <SelectTrigger class="w-full min-w-0 sm:w-[180px]">
                  <SelectValue :placeholder="t('admin.llmModels.memberProvider')" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="p in providers" :key="p.id" :value="p.id">
                    {{ p.name }}
                  </SelectItem>
                </SelectContent>
              </Select>
              <Input v-model="entry.model" :placeholder="t('admin.llmModels.memberModel')" class="min-w-0" />
              <Button size="icon" variant="ghost" class="shrink-0" @click="removeMemberEntry(i)">
                <Trash2 class="h-4 w-4" />
              </Button>
            </div>
          </div>
        </div>

        <DialogFooter class="flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button variant="outline" class="w-full sm:w-auto" @click="closeDialog">{{ t('common.cancel') }}</Button>
          <Button class="w-full sm:w-auto" :disabled="saving" @click="save">
            {{ saving ? t('common.saving') : (editing ? t('common.save') : t('admin.llmModels.create')) }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <Dialog :open="deleteOpen" @update:open="deleteOpen = false">
      <DialogContent class="w-[calc(100vw-2rem)] max-w-md">
        <DialogHeader>
          <DialogTitle>{{ t('admin.llmModels.deleteTitle') }}</DialogTitle>
          <DialogDescription>
            {{ t('admin.llmModels.deleteConfirm', { name: deleteTarget?.name ?? '' }) }}
          </DialogDescription>
        </DialogHeader>
        <DialogFooter class="flex-col-reverse gap-2 sm:flex-row sm:justify-end">
          <Button variant="outline" class="w-full sm:w-auto" @click="deleteOpen = false">{{ t('common.cancel') }}</Button>
          <Button variant="destructive" class="w-full sm:w-auto" :disabled="deleting" @click="deleteModel">
            {{ deleting ? t('admin.llmModels.deleting') : t('common.delete') }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
