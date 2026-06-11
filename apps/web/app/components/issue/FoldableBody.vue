<script setup lang="ts">
import { computed, ref, onMounted, watch, nextTick } from 'vue'
import MarkdownBody from '@/components/MarkdownBody.vue'
import CrossPostBadge from '@/components/issue/CrossPostBadge.vue'
import { useCrossPostComment } from '@/composables/useCrossPostComment'

const props = withDefaults(defineProps<{
  source: string
  /** Rendering height threshold (px). Content shorter than this isn't foldable. */
  maxHeight?: number
  /** Repo owner — required to render the cross-post badge link. */
  owner?: string
  /** Repo name — required to render the cross-post badge link. */
  name?: string
  /**
   * sourceIssueId from the API (source_issue_id). When > 0 this comment was
   * cross-posted from another issue. Preferred over the regex-based detection
   * from the blockquote prefix; the composable still strips the prefix from
   * the body text regardless.
   */
  sourceIssueId?: number
}>(), {
  maxHeight: 280,
  sourceIssueId: 0,
})

const { t } = useI18n()

// Detect cross-post attribution and strip it so the badge replaces the blockquote.
// When the API provides sourceIssueId (> 0), use it as the source of truth;
// otherwise fall back to regex-based detection from the blockquote prefix.
const crossPost = computed(() => {
  const parsed = useCrossPostComment(props.source)
  if (props.sourceIssueId > 0) {
    return {
      sourceIssueNumber: props.sourceIssueId,
      cleanBody: parsed?.cleanBody ?? props.source,
    }
  }
  return parsed
})
const displaySource = computed(() => crossPost.value?.cleanBody ?? props.source)

const containerRef = ref<HTMLElement | null>(null)
const needsFold = ref(false)
const expanded = ref(false)

async function measure() {
  await nextTick()
  if (containerRef.value) {
    needsFold.value = containerRef.value.scrollHeight > props.maxHeight
  }
}

onMounted(measure)

watch(() => props.source, () => {
  expanded.value = false
  measure()
})
</script>

<template>
  <div v-if="source">
    <!-- Cross-post badge: replaces the "> This comment was cross-posted from issue #N" blockquote -->
    <CrossPostBadge
      v-if="crossPost && owner && name"
      :source-issue-number="crossPost.sourceIssueNumber"
      :owner="owner"
      :name="name"
      class="mb-1"
    />
    <div
      ref="containerRef"
      :style="needsFold && !expanded ? { maxHeight: `${maxHeight}px`, overflow: 'hidden' } : {}"
      class="relative"
    >
      <MarkdownBody :source="displaySource" />
      <!-- Fade overlay at the bottom when content is clamped -->
      <div
        v-if="needsFold && !expanded"
        class="pointer-events-none absolute bottom-0 left-0 right-0 h-10 bg-gradient-to-t from-card to-transparent"
      />
    </div>
    <button
      v-if="needsFold"
      class="mt-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
      @click="expanded = !expanded"
    >
      {{ expanded ? t('issue.collapse') : t('issue.expand') }}
    </button>
  </div>
</template>
