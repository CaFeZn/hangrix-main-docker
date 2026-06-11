import { computed, ref, type ComputedRef, type Ref } from 'vue'
import type { VariableKind } from '~/types/repo'
import { parseDotenv, type DotenvEntry, type DotenvSkipped } from '@/lib/dotenv'

export type DiffStatus = 'create' | 'overwrite' | 'skip'

export interface PreviewRow {
  status: DiffStatus
  key?: string
  value?: string
  rawLine?: string
  reason?: string // i18n key suffix, e.g. 'comment', 'no_equals', 'empty_key', 'invalid_key_format'
  duplicate?: boolean
  index: number // 1-based original line number
}

export interface ImportSummary {
  created: string[]
  overwritten: string[]
  failed: Array<{ key: string; error: string }>
}

export function useDotenvImport(
  owner: () => string,
  name: () => string,
) {
  const text = ref('')
  const kind = ref<VariableKind>('plain')
  const submitting = ref(false)
  const fatalError = ref<string | null>(null)

  // Injected by caller: Set of existing key names for the currently-selected kind
  const existingNames = ref<Set<string>>(new Set())

  const preview = computed<PreviewRow[]>(() => {
    if (!text.value.trim()) return []

    const { entries, skipped } = parseDotenv(text.value)
    const rows: PreviewRow[] = []

    // Add skipped rows first (in line order)
    for (const s of skipped) {
      rows.push({
        status: 'skip',
        rawLine: s.rawLine,
        reason: s.reason,
        index: s.index,
      })
    }

    // Add entry rows
    for (const e of entries) {
      const exists = existingNames.value.has(e.key)
      rows.push({
        status: exists ? 'overwrite' : 'create',
        key: e.key,
        value: e.value,
        duplicate: e.duplicate,
        index: e.index,
      })
    }

    // Sort by original line index
    rows.sort((a, b) => a.index - b.index)

    return rows
  })

  const counts = computed(() => {
    let create = 0
    let overwrite = 0
    let skip = 0
    for (const r of preview.value) {
      if (r.status === 'create') create++
      else if (r.status === 'overwrite') overwrite++
      else skip++
    }
    return {
      create,
      overwrite,
      skip,
      total: preview.value.length,
      valid: create + overwrite,
    }
  })

  async function submit(): Promise<ImportSummary> {
    const summary: ImportSummary = { created: [], overwritten: [], failed: [] }
    submitting.value = true
    fatalError.value = null

    // Build the effective (deduplicated) set from the parse result
    const { entries } = parseDotenv(text.value)
    // Only use the last occurrence of each key (non-duplicate entries)
    const effective = entries.filter(e => !e.duplicate)

    try {
      for (const e of effective) {
        const o = owner()
        const n = name()
        const exists = existingNames.value.has(e.key)

        try {
          if (exists) {
            // Overwrite → PATCH
            await $fetch(
              `/api/repos/${o}/${n}/variables/${encodeURIComponent(e.key)}`,
              {
                method: 'PATCH',
                credentials: 'include',
                body: { value: e.value, kind: kind.value },
              },
            )
            summary.overwritten.push(e.key)
          } else {
            // Create → POST
            await $fetch(`/api/repos/${o}/${n}/variables`, {
              method: 'POST',
              credentials: 'include',
              body: { name: e.key, value: e.value, kind: kind.value },
            })
            summary.created.push(e.key)
          }
        } catch (err: any) {
          summary.failed.push({
            key: e.key,
            error: err?.data?.error ?? (err?.status === 404 ? 'not_found' : 'unknown'),
          })
        }
      }
    } catch (e: any) {
      fatalError.value = e?.data?.error ?? 'Network error'
    } finally {
      submitting.value = false
    }

    return summary
  }

  return {
    text,
    kind,
    existingNames,
    preview,
    counts,
    submit,
    submitting,
    fatalError,
  }
}
