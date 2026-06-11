<script setup lang="ts">
import type { HTMLAttributes } from "vue"
import { cn } from '@/utils/utils'

const props = withDefaults(defineProps<{
  class?: HTMLAttributes["class"]
  variant?: "default" | "destructive"
}>(), {
  variant: "default",
})
</script>

<template>
  <div
    data-slot="alert"
    role="alert"
    :class="cn(
      'relative w-full rounded-lg border px-4 py-3 text-sm [&>svg+div]:translate-y-[-3px] [&>svg]:absolute [&>svg]:left-4 [&>svg]:top-3.5 [&>svg]:text-foreground [&>svg~*]:pl-7',
      variant === 'destructive' && 'border-destructive/50 text-destructive dark:border-destructive [&>svg]:text-destructive',
      props.class,
    )"
  >
    <slot />
  </div>
</template>
