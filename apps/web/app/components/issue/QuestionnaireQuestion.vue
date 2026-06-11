<script setup lang="ts">
import type { AnswerEntry, Question } from '~/types/questionnaire'
import { OTHER_OPTION_ID } from '~/types/questionnaire'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Checkbox } from '@/components/ui/checkbox'
import { Textarea } from '@/components/ui/textarea'
import { Label } from '@/components/ui/label'
import { Input } from '@/components/ui/input'

const { t } = useI18n()

const props = defineProps<{
  question: Question
  modelValue: AnswerEntry
  error?: string | null
}>()

const emit = defineEmits<{
  'update:modelValue': [value: AnswerEntry]
}>()

function isOtherSelected(): boolean {
  return (props.modelValue.option_ids ?? []).includes(OTHER_OPTION_ID)
}

function emitFull(option_ids: string[], text?: string) {
  const entry: AnswerEntry = {
    question_id: props.question.id,
    option_ids,
    text: text ?? props.modelValue.text,
  }
  emit('update:modelValue', entry)
}

const selectedOptionId = computed({
  get: () => props.modelValue.option_ids?.[0] ?? '',
  set: (val: string) => {
    const wasOther = isOtherSelected()
    if (wasOther && val !== OTHER_OPTION_ID) {
      emitFull(val ? [val] : [], '')
    } else {
      emitFull(val ? [val] : [])
    }
  },
})

const selectedOptionIds = computed({
  get: () => new Set(props.modelValue.option_ids ?? []),
  set: (val: Set<string>) => {
    emitFull([...val])
  },
})

function toggleOption(optId: string) {
  const next = new Set(selectedOptionIds.value)
  if (next.has(optId)) {
    next.delete(optId)
    if (optId === OTHER_OPTION_ID) {
      emitFull([...next], '')
    } else {
      emitFull([...next])
    }
  } else {
    next.add(optId)
    emitFull([...next])
  }
}

const otherText = computed({
  get: () => props.modelValue.text ?? '',
  set: (val: string) => {
    emit('update:modelValue', {
      question_id: props.question.id,
      option_ids: props.modelValue.option_ids,
      text: val,
    })
  },
})

const textValue = computed({
  get: () => props.modelValue.text ?? '',
  set: (val: string) => {
    emit('update:modelValue', {
      question_id: props.question.id,
      text: val,
    })
  },
})
</script>

<template>
  <div class="space-y-2">
    <p class="text-sm font-medium">
      {{ question.text }}
      <span v-if="question.required" class="text-destructive">*</span>
    </p>

    <!-- Single choice -->
    <template v-if="question.type === 'single_choice'">
      <RadioGroup v-model="selectedOptionId" class="space-y-1.5">
        <div
          v-for="opt in question.options"
          :key="opt.id"
        >
          <div class="flex items-center gap-2">
            <RadioGroupItem :id="`q${question.id}-${opt.id}`" :value="opt.id" />
            <Label :for="`q${question.id}-${opt.id}`" class="text-sm font-normal cursor-pointer">
              {{ opt.label }}
            </Label>
          </div>
          <div
            v-if="opt.id === OTHER_OPTION_ID && selectedOptionId === OTHER_OPTION_ID"
            class="ml-6 mt-1"
          >
            <Input
              v-model="otherText"
              :placeholder="t('issue.questionnaire.otherPlaceholder')"
              :aria-label="t('issue.questionnaire.otherPlaceholder')"
              class="text-sm"
            />
          </div>
        </div>
      </RadioGroup>
    </template>

    <!-- Multi choice -->
    <template v-else-if="question.type === 'multi_choice'">
      <div class="space-y-1.5">
        <div
          v-for="opt in question.options"
          :key="opt.id"
        >
          <div class="flex items-center gap-2">
            <Checkbox
              :id="`q${question.id}-${opt.id}`"
              :model-value="selectedOptionIds.has(opt.id)"
              @update:model-value="() => toggleOption(opt.id)"
            />
            <Label :for="`q${question.id}-${opt.id}`" class="text-sm font-normal cursor-pointer">
              {{ opt.label }}
            </Label>
          </div>
          <div
            v-if="opt.id === OTHER_OPTION_ID && selectedOptionIds.has(OTHER_OPTION_ID)"
            class="ml-6 mt-1"
          >
            <Input
              v-model="otherText"
              :placeholder="t('issue.questionnaire.otherPlaceholder')"
              :aria-label="t('issue.questionnaire.otherPlaceholder')"
              class="text-sm"
            />
          </div>
        </div>
      </div>
    </template>

    <!-- Text input -->
    <template v-else>
      <Textarea
        v-model="textValue"
        :placeholder="question.required ? t('issue.questionnaire.placeholderRequired') : t('issue.questionnaire.placeholderOptional')"
        class="min-h-20 resize-y text-sm"
        rows="3"
      />
    </template>

    <p v-if="error" class="text-xs text-destructive">{{ error }}</p>
  </div>
</template>
