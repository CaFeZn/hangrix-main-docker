<script setup lang="ts">
import { computed } from 'vue'
import { ChevronLeft, ChevronRight } from 'lucide-vue-next'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

interface Props {
  total: number
  offset: number
  limit: number
  pageSizeOptions?: readonly number[]
}

const props = withDefaults(defineProps<Props>(), {
  pageSizeOptions: undefined,
})

const emit = defineEmits<{
  (e: 'update:offset', value: number): void
  (e: 'update:limit', value: number): void
}>()

const { t } = useI18n()

const pageCount = computed(() => Math.max(1, Math.ceil(props.total / Math.max(1, props.limit))))
const currentPage = computed(() => Math.floor(props.offset / Math.max(1, props.limit)) + 1)

const from = computed(() => (props.total === 0 ? 0 : props.offset + 1))
const to = computed(() => Math.min(props.total, props.offset + props.limit))

const canPrev = computed(() => props.offset > 0)
const canNext = computed(() => props.offset + props.limit < props.total)

const limitStr = computed(() => String(props.limit))

function prev() {
  if (!canPrev.value) return
  emit('update:offset', Math.max(0, props.offset - props.limit))
}
function next() {
  if (!canNext.value) return
  emit('update:offset', props.offset + props.limit)
}

function onPageSizeChange(value: unknown) {
  emit('update:limit', Number(value))
}
</script>

<template>
  <div v-if="total > limit" class="flex items-center justify-between gap-3 text-sm">
    <div class="flex items-center gap-2">
      <template v-if="pageSizeOptions">
        <span class="text-muted-foreground">{{ t('common.pagination.pageSize') }}</span>
        <Select :model-value="limitStr" @update:model-value="onPageSizeChange">
          <SelectTrigger class="h-8 w-[70px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="opt in pageSizeOptions" :key="opt" :value="String(opt)">
              {{ opt }}
            </SelectItem>
          </SelectContent>
        </Select>
      </template>
      <span class="text-muted-foreground tabular-nums">
        {{ t('common.pagination.summary', { from, to, total }) }}
      </span>
    </div>
    <div class="flex items-center gap-2">
      <span class="text-xs text-muted-foreground tabular-nums">
        {{ t('common.pagination.page', { page: currentPage, total: pageCount }) }}
      </span>
      <Button size="sm" variant="outline" :disabled="!canPrev" @click="prev">
        <ChevronLeft class="size-4" />
        {{ t('common.pagination.prev') }}
      </Button>
      <Button size="sm" variant="outline" :disabled="!canNext" @click="next">
        {{ t('common.pagination.next') }}
        <ChevronRight class="size-4" />
      </Button>
    </div>
  </div>
</template>
