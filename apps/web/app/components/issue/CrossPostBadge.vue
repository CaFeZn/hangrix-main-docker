<script setup lang="ts">
import { ArrowRightLeft } from 'lucide-vue-next'
import { Badge } from '@/components/ui/badge'

defineProps<{
  sourceIssueNumber: number
  owner: string
  name: string
}>()

const { t } = useI18n()
</script>

<template>
  <NuxtLink
    :to="`/${owner}/${name}/issues/${sourceIssueNumber}`"
    class="inline-flex"
  >
    <Badge
      variant="secondary"
      class="cursor-pointer transition-colors hover:bg-secondary/80 font-normal gap-1"
    >
      <ArrowRightLeft class="size-3" />
      {{ t('issue.crossPostFrom', { n: sourceIssueNumber }) }}
    </Badge>
  </NuxtLink>
</template>
