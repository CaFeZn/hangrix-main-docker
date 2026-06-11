<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  AlertTriangle,
  Clock,
  Loader2,
  Plus,
  Shield,
  VolumeX,
  X,
} from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { RepoSilence, SilenceAuditEntry, SilenceOverride } from '~/types/silence'

const props = defineProps<{
  owner: string
  name: string
}>()

const { t } = useI18n()

const state = ref<RepoSilence | null>(null)
const overrides = ref<SilenceOverride[]>([])
const audit = ref<SilenceAuditEntry[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const actionError = ref<string | null>(null)
const actionBusy = ref(false)
const actionSuccess = ref<string | null>(null)

// Enter dialog state
const enterOpen = ref(false)
const enterReason = ref('')
const enterDuration = ref('') // duration string e.g. "2h"
const enterDurationError = ref<string | null>(null)

// Override dialog state
const overrideOpen = ref(false)
const overrideSessionId = ref('')
const overrideReason = ref('')

async function loadAll() {
  loading.value = true
  error.value = null
  try {
    const [s, o, a] = await Promise.all([
      $fetch<RepoSilence>(`/api/repos/${props.owner}/${props.name}/silence`, {
        credentials: 'include',
      }).catch(() => null),
      $fetch<{ items: SilenceOverride[] }>(`/api/repos/${props.owner}/${props.name}/silence/overrides`, {
        credentials: 'include',
      }).then(r => r.items).catch(() => [] as SilenceOverride[]),
      $fetch<{ items: SilenceAuditEntry[] }>(`/api/repos/${props.owner}/${props.name}/silence/audit?limit=20`, {
        credentials: 'include',
      }).then(r => r.items).catch(() => [] as SilenceAuditEntry[]),
    ])
    state.value = s
    overrides.value = o ?? []
    audit.value = a ?? []
  } catch (e: any) {
    error.value = e?.data?.error ?? 'Failed to load silence state'
  } finally {
    loading.value = false
  }
}

async function onEnter() {
  if (!enterDuration.value.trim()) return
  actionBusy.value = true
  actionError.value = null
  actionSuccess.value = null
  try {
    await $fetch(`/api/repos/${props.owner}/${props.name}/silence/enter`, {
      method: 'POST',
      credentials: 'include',
      body: {
        reason: enterReason.value.trim() || undefined,
        duration: enterDuration.value.trim(),
      },
    })
    enterOpen.value = false
    enterReason.value = ''
    enterDuration.value = ''
    actionSuccess.value = t('repo.silence.entered')
    await loadAll()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? 'Enter silence failed'
  } finally {
    actionBusy.value = false
  }
}

async function onExit() {
  if (!confirm(t('repo.silence.exitConfirm'))) return
  actionBusy.value = true
  actionError.value = null
  actionSuccess.value = null
  try {
    await $fetch(`/api/repos/${props.owner}/${props.name}/silence/exit`, {
      method: 'POST',
      credentials: 'include',
      body: {},
    })
    actionSuccess.value = t('repo.silence.exited')
    await loadAll()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? 'Exit silence failed'
  } finally {
    actionBusy.value = false
  }
}

async function onGrantOverride() {
  const sid = Number(overrideSessionId.value.trim())
  if (!sid) return
  actionBusy.value = true
  actionError.value = null
  try {
    await $fetch(`/api/repos/${props.owner}/${props.name}/silence/override`, {
      method: 'POST',
      credentials: 'include',
      body: {
        session_id: sid,
        reason: overrideReason.value.trim() || undefined,
      },
    })
    overrideOpen.value = false
    overrideSessionId.value = ''
    overrideReason.value = ''
    await loadAll()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? 'Override grant failed'
  } finally {
    actionBusy.value = false
  }
}

async function onRevokeOverride(sessionId: number) {
  if (!confirm(t('repo.silence.revokeConfirm', { session: sessionId }))) return
  actionBusy.value = true
  actionError.value = null
  try {
    await $fetch(
      `/api/repos/${props.owner}/${props.name}/silence/override/${sessionId}`,
      { method: 'DELETE', credentials: 'include' },
    )
    await loadAll()
  } catch (e: any) {
    actionError.value = e?.data?.error ?? 'Override revoke failed'
  } finally {
    actionBusy.value = false
  }
}

const isSilenced = computed(() => state.value?.active === true)

const sourceLabel = computed(() => {
  const s = state.value
  if (!s || !s.source) return '—'
  switch (s.source) {
    case 'manual': return t('repo.silence.sourceManual')
    case 'schedule': return t('repo.silence.sourceSchedule')
    case 'api': return t('repo.silence.sourceApi')
    default: return s.source
  }
})

const enteredAtLabel = computed(() => {
  if (!state.value?.entered_at) return '—'
  return new Date(state.value.entered_at).toLocaleString()
})

const expectedExitLabel = computed(() => {
  if (!state.value?.expected_exit_at) return t('repo.silence.noExpectedExit')
  return new Date(state.value.expected_exit_at).toLocaleString()
})

function eventLabel(evt: string): string {
  switch (evt) {
    case 'entered': return t('repo.silence.eventEntered')
    case 'exited': return t('repo.silence.eventExited')
    case 'override_granted': return t('repo.silence.eventOverrideGranted')
    case 'override_revoked': return t('repo.silence.eventOverrideRevoked')
    case 'suspended': return t('repo.silence.eventSuspended')
    case 'resumed': return t('repo.silence.eventResumed')
    default: return evt
  }
}

onMounted(loadAll)
watch([() => props.owner, () => props.name], loadAll)
</script>

<template>
  <div class="space-y-6">
    <!-- Error / success banners -->
    <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>
    <p v-if="actionSuccess" class="text-sm text-emerald-500">{{ actionSuccess }}</p>
    <p v-if="error" class="text-sm text-destructive">{{ error }}</p>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center gap-2 text-sm text-muted-foreground py-4">
      <Loader2 class="size-4 animate-spin" />
      {{ t('common.loading') }}
    </div>

    <template v-else>
      <!-- Current State Card -->
      <Card>
        <CardHeader>
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="space-y-1">
              <CardTitle class="flex items-center gap-2">
                <VolumeX class="size-5" />
                {{ t('repo.silence.currentState') }}
              </CardTitle>
              <CardDescription>{{ t('repo.silence.currentStateHint') }}</CardDescription>
            </div>
            <div class="flex items-center gap-2">
              <Button
                v-if="!isSilenced"
                size="sm"
                :disabled="actionBusy"
                @click="enterOpen = true"
              >
                <Plus class="size-4" />
                {{ t('repo.silence.enterManual') }}
              </Button>
              <Button
                v-else
                variant="destructive"
                size="sm"
                :disabled="actionBusy"
                @click="onExit"
              >
                <X class="size-4" />
                {{ t('repo.silence.exitSilence') }}
              </Button>
            </div>
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <TableBody>
              <TableRow>
                <TableCell class="w-32 font-medium text-muted-foreground">
                  {{ t('repo.silence.status') }}
                </TableCell>
                <TableCell>
                  <Badge :variant="isSilenced ? 'destructive' : 'secondary'">
                    {{ isSilenced ? t('repo.silence.active') : t('repo.silence.inactive') }}
                  </Badge>
                </TableCell>
              </TableRow>
              <TableRow v-if="isSilenced">
                <TableCell class="font-medium text-muted-foreground">
                  {{ t('repo.silence.source') }}
                </TableCell>
                <TableCell>
                  <div class="flex items-center gap-2">
                    <Shield v-if="state?.source === 'manual'" class="size-4 text-muted-foreground" />
                    <Clock v-else class="size-4 text-muted-foreground" />
                    {{ sourceLabel }}
                    <span v-if="state?.source_ref" class="text-sm text-muted-foreground">
                      ({{ state.source_ref }})
                    </span>
                  </div>
                </TableCell>
              </TableRow>
              <TableRow v-if="isSilenced">
                <TableCell class="font-medium text-muted-foreground">
                  {{ t('repo.silence.enteredAt') }}
                </TableCell>
                <TableCell class="text-sm">{{ enteredAtLabel }}</TableCell>
              </TableRow>
              <TableRow v-if="isSilenced">
                <TableCell class="font-medium text-muted-foreground">
                  {{ t('repo.silence.expectedExit') }}
                </TableCell>
                <TableCell class="text-sm">{{ expectedExitLabel }}</TableCell>
              </TableRow>
              <TableRow v-if="isSilenced && state?.reason">
                <TableCell class="font-medium text-muted-foreground">
                  {{ t('repo.silence.reason') }}
                </TableCell>
                <TableCell class="text-sm">{{ state!.reason }}</TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <!-- Active Overrides Card -->
      <Card>
        <CardHeader class="flex flex-row items-center justify-between gap-2">
          <div class="space-y-1">
            <CardTitle>{{ t('repo.silence.overrides') }}</CardTitle>
            <CardDescription>{{ t('repo.silence.overridesHint') }}</CardDescription>
          </div>
          <Button size="sm" variant="outline" :disabled="actionBusy" @click="overrideOpen = true">
            <Plus class="size-4" />
            {{ t('repo.silence.grantOverride') }}
          </Button>
        </CardHeader>
        <CardContent>
          <p v-if="overrides.length === 0" class="text-sm text-muted-foreground">
            {{ t('repo.silence.noOverrides') }}
          </p>
          <Table v-else>
            <TableHeader>
              <TableRow>
                <TableHead>{{ t('repo.silence.sessionId') }}</TableHead>
                <TableHead>{{ t('repo.silence.grantedBy') }}</TableHead>
                <TableHead>{{ t('repo.silence.grantedAt') }}</TableHead>
                <TableHead>{{ t('repo.silence.reason') }}</TableHead>
                <TableHead class="w-12 text-right" />
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="o in overrides" :key="o.session_id">
                <TableCell class="font-mono text-sm">#{{ o.session_id }}</TableCell>
                <TableCell class="text-sm">#{{ o.granted_by }}</TableCell>
                <TableCell class="text-sm text-muted-foreground">
                  {{ new Date(o.granted_at).toLocaleString() }}
                </TableCell>
                <TableCell class="text-sm max-w-48 truncate">
                  {{ o.reason || '—' }}
                </TableCell>
                <TableCell class="text-right">
                  <Button
                    variant="ghost"
                    size="icon"
                    :disabled="actionBusy"
                    @click="onRevokeOverride(o.session_id)"
                  >
                    <X class="size-4" />
                  </Button>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <!-- Audit Log Card -->
      <Card>
        <CardHeader>
          <CardTitle>{{ t('repo.silence.auditLog') }}</CardTitle>
          <CardDescription>{{ t('repo.silence.auditLogHint') }}</CardDescription>
        </CardHeader>
        <CardContent>
          <p v-if="audit.length === 0" class="text-sm text-muted-foreground">
            {{ t('repo.silence.noAudit') }}
          </p>
          <Table v-else>
            <TableHeader>
              <TableRow>
                <TableHead>{{ t('repo.silence.event') }}</TableHead>
                <TableHead>{{ t('repo.silence.source') }}</TableHead>
                <TableHead>{{ t('repo.silence.timestamp') }}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="e in audit" :key="e.id">
                <TableCell>
                  <Badge variant="outline" class="text-xs">{{ eventLabel(e.event) }}</Badge>
                </TableCell>
                <TableCell class="text-sm text-muted-foreground">{{ e.source }}</TableCell>
                <TableCell class="text-sm text-muted-foreground">
                  {{ new Date(e.created_at).toLocaleString() }}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <!-- Enter Silence Dialog -->
      <Dialog v-model:open="enterOpen">
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{{ t('repo.silence.enterManualTitle') }}</DialogTitle>
            <DialogDescription>{{ t('repo.silence.enterManualHint') }}</DialogDescription>
          </DialogHeader>
          <div class="space-y-4">
            <div class="space-y-2">
              <Label for="silence-duration">{{ t('repo.silence.duration') }}</Label>
              <Input
                id="silence-duration"
                v-model="enterDuration"
                placeholder="2h"
                autocomplete="off"
              />
              <p class="text-xs text-muted-foreground">{{ t('repo.silence.durationHint') }}</p>
            </div>
            <div class="space-y-2">
              <Label for="silence-reason">{{ t('repo.silence.reason') }} <span class="text-muted-foreground">({{ t('repo.silence.optional') }})</span></Label>
              <Input
                id="silence-reason"
                v-model="enterReason"
                autocomplete="off"
                :placeholder="t('repo.silence.reasonPlaceholder')"
              />
            </div>
            <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>
            <div class="flex items-start gap-3 rounded-md border border-amber-500/30 bg-amber-50 p-3 dark:bg-amber-950/20">
              <AlertTriangle class="size-5 shrink-0 text-amber-600 mt-0.5" />
              <p class="text-sm text-amber-800 dark:text-amber-400">
                {{ t('repo.silence.enterWarning') }}
              </p>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="enterOpen = false" :disabled="actionBusy">
              {{ t('common.cancel') }}
            </Button>
            <Button
              :disabled="!enterDuration.trim() || actionBusy"
              @click="onEnter"
            >
              {{ actionBusy ? t('common.submitting') : t('repo.silence.enterSilence') }}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <!-- Grant Override Dialog -->
      <Dialog v-model:open="overrideOpen">
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{{ t('repo.silence.grantOverride') }}</DialogTitle>
            <DialogDescription>{{ t('repo.silence.grantOverrideHint') }}</DialogDescription>
          </DialogHeader>
          <div class="space-y-4">
            <div class="space-y-2">
              <Label for="override-session">{{ t('repo.silence.sessionId') }}</Label>
              <Input
                id="override-session"
                v-model="overrideSessionId"
                type="number"
                autocomplete="off"
                placeholder="123"
              />
            </div>
            <div class="space-y-2">
              <Label for="override-reason">{{ t('repo.silence.reason') }} <span class="text-muted-foreground">({{ t('repo.silence.optional') }})</span></Label>
              <Input
                id="override-reason"
                v-model="overrideReason"
                autocomplete="off"
                :placeholder="t('repo.silence.reasonPlaceholder')"
              />
            </div>
            <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>
          </div>
          <DialogFooter>
            <Button variant="outline" @click="overrideOpen = false" :disabled="actionBusy">
              {{ t('common.cancel') }}
            </Button>
            <Button
              :disabled="!overrideSessionId.trim() || actionBusy"
              @click="onGrantOverride"
            >
              {{ actionBusy ? t('common.submitting') : t('repo.silence.grantOverride') }}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </template>
  </div>
</template>
