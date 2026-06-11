<script setup lang="ts">
import { AtSign, MessageCircleQuestion } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'
import type { IssueIndicators } from '~/types/issue'

defineProps<{
  indicators?: IssueIndicators
}>()

const { t } = useI18n()
</script>

<template>
  <span v-if="indicators?.pending_questionnaire || indicators?.agent_mention" class="flex items-center gap-1">
    <Badge v-if="indicators?.pending_questionnaire"
           class="gap-1 bg-amber-500/15 text-amber-700 dark:text-amber-300"
           variant="secondary">
      <MessageCircleQuestion class="size-3" />
      {{ t('issue.indicators.pendingQuestionnaire') }}
    </Badge>
    <Badge v-if="indicators?.agent_mention"
           class="gap-1 bg-sky-500/15 text-sky-700 dark:text-sky-300"
           variant="secondary">
      <AtSign class="size-3" />
      {{ t('issue.indicators.mentioned') }}
    </Badge>
  </span>
</template>
