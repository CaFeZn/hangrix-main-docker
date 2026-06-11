<script setup lang="ts">
import { watch } from 'vue'
import {
  AlertTriangle,
  CheckCircle2,
  Eye,
  EyeOff,
  FileText,
  XCircle,
} from 'lucide-vue-next'
import type { VariableKind } from '~/types/repo'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { useDotenvImport, type ImportSummary } from '@/composables/useDotenvImport'

const props = defineProps<{
  open: boolean
  owner: string
  name: string
  initialKind: VariableKind
  existingVariableNames: string[]
  existingSecretNames: string[]
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  'imported': [summary: ImportSummary]
}>()

const { t } = useI18n()

const {
  text,
  kind,
  existingNames,
  preview,
  counts,
  submit,
  submitting,
} = useDotenvImport(() => props.owner, () => props.name)

const step = ref<'input' | 'preview' | 'submitting'>('input')

// Reveal tracking per row index
const revealedRows = ref<Set<number>>(new Set())

function toggleRevealRow(index: number) {
  const next = new Set(revealedRows.value)
  if (next.has(index)) next.delete(index)
  else next.add(index)
  revealedRows.value = next
}

// Sync kind from prop on open
watch(() => props.open, (open) => {
  if (open) {
    kind.value = props.initialKind
    text.value = ''
    step.value = 'input'
    revealedRows.value = new Set()
  }
})

// Sync existing names when kind changes
watch(kind, (k) => {
  if (k === 'plain') {
    existingNames.value = new Set(props.existingVariableNames)
  } else {
    existingNames.value = new Set(props.existingSecretNames)
  }
}, { immediate: true })

// Also sync when existing lists change
watch([() => props.existingVariableNames, () => props.existingSecretNames], () => {
  if (kind.value === 'plain') {
    existingNames.value = new Set(props.existingVariableNames)
  } else {
    existingNames.value = new Set(props.existingSecretNames)
  }
})

function goToPreview() {
  if (!text.value.trim()) return
  step.value = 'preview'
  revealedRows.value = new Set()
}

function goBack() {
  step.value = 'input'
}

function close() {
  emit('update:open', false)
}

async function doImport() {
  step.value = 'submitting'
  const result = await submit()
  emit('imported', result)
  close()
}

function skipReasonLabel(reason: string): string {
  return t(`repo.variables.import.${reason}`)
}

function statusBadge(status: string) {
  switch (status) {
    case 'create':
      return { variant: 'default' as const, label: t('repo.variables.import.previewCreate') }
    case 'overwrite':
      return { variant: 'secondary' as const, label: t('repo.variables.import.previewOverwrite') }
    case 'skip':
      return { variant: 'outline' as const, label: t('repo.variables.import.previewSkip') }
    default:
      return { variant: 'outline' as const, label: status }
  }
}

function rowClass(status: string) {
  switch (status) {
    case 'create': return 'text-emerald-700 dark:text-emerald-400'
    case 'overwrite': return 'text-amber-700 dark:text-amber-400'
    case 'skip': return 'text-muted-foreground line-through'
    default: return ''
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="max-w-2xl">
      <DialogHeader>
        <DialogTitle>{{ t('repo.variables.import.title') }}</DialogTitle>
        <DialogDescription>
          {{ t('repo.variables.import.description') }}
        </DialogDescription>
      </DialogHeader>

      <!-- Step: input -->
      <div v-if="step === 'input'" class="space-y-4">
        <Tabs v-model="kind" class="w-full">
          <TabsList>
            <TabsTrigger value="plain">
              {{ t('repo.variables.kindPlain') }}
            </TabsTrigger>
            <TabsTrigger value="secret">
              {{ t('repo.variables.kindSecret') }}
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <Textarea
          v-model="text"
          :rows="14"
          class="font-mono text-sm"
          :placeholder="t('repo.variables.import.textareaPlaceholder')"
        />

        <div class="flex justify-end gap-2">
          <Button variant="outline" @click="close">
            {{ t('common.cancel') }}
          </Button>
          <Button @click="goToPreview" :disabled="!text.trim()">
            {{ t('repo.variables.import.previewButton') }}
          </Button>
        </div>
      </div>

      <!-- Step: preview -->
      <div v-if="step === 'preview'" class="space-y-4">
        <!-- Summary -->
        <div class="flex flex-wrap items-center gap-3 text-sm">
          <span class="text-muted-foreground">
            {{ t('repo.variables.import.summaryTotal', { n: counts.total }) }}
          </span>
          <span class="flex items-center gap-1 text-emerald-600">
            <CheckCircle2 class="size-4" />
            {{ t('repo.variables.import.summaryCreate', { n: counts.create }) }}
          </span>
          <span class="flex items-center gap-1 text-amber-600">
            <AlertTriangle class="size-4" />
            {{ t('repo.variables.import.summaryOverwrite', { n: counts.overwrite }) }}
          </span>
          <span class="flex items-center gap-1 text-muted-foreground">
            <XCircle class="size-4" />
            {{ t('repo.variables.import.summarySkip', { n: counts.skip }) }}
          </span>
        </div>

        <!-- Table -->
        <div class="max-h-80 overflow-auto rounded-md border min-w-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-8">#</TableHead>
                <TableHead>{{ t('repo.variables.import.colKey') }}</TableHead>
                <TableHead>{{ t('repo.variables.import.colValue') }}</TableHead>
                <TableHead class="w-28">{{ t('repo.variables.import.colStatus') }}</TableHead>
                <TableHead class="w-10" />
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow
                v-for="(row, idx) in preview"
                :key="idx"
                :class="rowClass(row.status)"
              >
                <TableCell class="text-xs text-muted-foreground tabular-nums">
                  {{ row.index }}
                </TableCell>
                <TableCell class="font-mono text-sm whitespace-normal break-all">
                  {{ row.key ?? '' }}
                </TableCell>
                <TableCell class="font-mono text-sm whitespace-normal break-all">
                  <template v-if="row.status === 'skip'">
                    <span class="text-muted-foreground italic">{{ row.rawLine }}</span>
                  </template>
                  <template v-else>
                    <span v-if="revealedRows.has(row.index)">{{ row.value }}</span>
                    <span v-else class="text-muted-foreground">••••••••</span>
                  </template>
                </TableCell>
                <TableCell>
                  <Badge :variant="statusBadge(row.status).variant">
                    {{ statusBadge(row.status).label }}
                  </Badge>
                  <span v-if="row.duplicate" class="ml-1 text-xs text-muted-foreground">
                    {{ t('repo.variables.import.duplicate') }}
                  </span>
                </TableCell>
                <TableCell>
                  <Button
                    v-if="row.status !== 'skip'"
                    variant="ghost"
                    size="icon"
                    class="size-7"
                    @click="toggleRevealRow(row.index)"
                  >
                    <Eye v-if="!revealedRows.has(row.index)" class="size-3.5" />
                    <EyeOff v-else class="size-3.5" />
                  </Button>
                </TableCell>
              </TableRow>
              <TableRow v-if="preview.length === 0">
                <TableCell colspan="5" class="text-center text-muted-foreground py-8">
                  {{ t('repo.variables.import.emptyPreview') }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>

        <DialogFooter class="gap-2">
          <Button variant="outline" @click="goBack">
            {{ t('repo.variables.import.backToEdit') }}
          </Button>
          <Button variant="outline" @click="close">
            {{ t('common.cancel') }}
          </Button>
          <Button @click="doImport" :disabled="counts.valid === 0">
            <FileText class="size-4" />
            {{ t('repo.variables.import.confirmImport') }}
          </Button>
        </DialogFooter>
      </div>

      <!-- Step: submitting -->
      <div v-if="step === 'submitting'" class="flex items-center justify-center py-8">
        <p class="text-sm text-muted-foreground">
          {{ t('repo.variables.import.importing') }}
        </p>
      </div>
    </DialogContent>
  </Dialog>
</template>
