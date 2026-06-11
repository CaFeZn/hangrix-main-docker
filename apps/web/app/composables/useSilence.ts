import type { RepoSilence, SessionSilenceView } from '~/types/silence'

// useSilence extracts silence state for a repository. The architecture
// embeds silence data in the repo GET response; this composable wraps
// a separate fetch to /api/repos/{owner}/{name}/silence as a fallback
// until the DTO is extended.
export function useSilence(owner: () => string, name: () => string) {
  const key = computed(() => `silence:${owner()}/${name()}`)

  const state = useState<RepoSilence | null>(key.value, () => null)
  const error = useState<string | null>(`${key.value}:error`, () => null)
  const loading = useState<boolean>(`${key.value}:loading`, () => false)

  async function load(force = false) {
    if (state.value && !force) return state.value
    if (loading.value) return state.value
    loading.value = true
    error.value = null
    try {
      const data = await $fetch<RepoSilence>(
        `/api/repos/${owner()}/${name()}/silence`,
        { credentials: 'include' },
      )
      state.value = data
      return data
    } catch (e: any) {
      // 404 / not-found is normal — silence module may not be loaded
      // or the endpoint is not yet wired. Treat as "not silenced".
      if (e?.status === 404 || e?.statusCode === 404) {
        state.value = null
        return null
      }
      error.value = e?.data?.error ?? 'Failed to load silence state'
      state.value = null
      return null
    } finally {
      loading.value = false
    }
  }

  // Re-fetch if route params change
  watch(key, async () => {
    state.value = null
    error.value = null
    await load(true)
  })

  return { state, error, loading, load }
}

// deriveSilenceBanner returns display properties for the silence banner
// from a RepoSilence state. Returns null when there's nothing to show.
export function deriveSilenceBanner(s: RepoSilence | null) {
  if (!s || !s.active) return null

  const exitTime = s.expected_exit_at
    ? new Date(s.expected_exit_at)
    : null

  return {
    variant: (s.source === 'manual' ? 'destructive' : 'warning') as 'destructive' | 'warning',
    source: s.source,
    sourceRef: s.source_ref,
    reason: s.reason,
    exitTime,
    hasOverride: false, // populated by caller context
  }
}

// deriveSessionSilence returns display properties for a session's
// silence badge.
export function deriveSessionSilence(v: SessionSilenceView | null | undefined) {
  if (!v) return null
  return {
    silenced: v.silenced,
    overridden: v.overridden,
    expectedExitAt: v.expected_exit_at ? new Date(v.expected_exit_at) : null,
  }
}
