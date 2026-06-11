<script setup lang="ts">
// SkillTitleInput wraps a plain <input> with a "/"-triggered
// autocomplete popover for skills loaded from .hangrix/skills/.
// When the user selects a skill the input is replaced with "/<slug> "
// and a "skill" emit is fired.
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { cn } from '@/utils/utils'
import type { Skill } from '~/types/skill'

const props = withDefaults(
  defineProps<{
    modelValue: string
    skills: Skill[]
    placeholder?: string
    disabled?: boolean
  }>(),
  {},
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'skill', payload: { slug: string; supplement: string } | null): void
}>()

const inputRef = ref<HTMLInputElement | null>(null)

// Autocomplete state
const open = ref(false)
const triggerStart = ref(-1)
const filter = ref('')
const activeIndex = ref(0)
const selectedSlug = ref<string | null>(null)

// Popover position
const popoverTop = ref(0)
const popoverLeft = ref(0)

const filtered = computed<Skill[]>(() => {
  const q = filter.value.toLowerCase()
  if (!q) return props.skills.slice(0, 8)
  const matches = props.skills.filter((s) =>
    s.slug.toLowerCase().includes(q),
  )
  return matches.slice(0, 8)
})

watch(filtered, () => {
  if (activeIndex.value >= filtered.value.length) {
    activeIndex.value = Math.max(0, filtered.value.length - 1)
  }
})

function closeDropdown() {
  open.value = false
  triggerStart.value = -1
  filter.value = ''
  activeIndex.value = 0
}

function detectSlash(value: string, caret: number): { start: number; filter: string } | null {
  if (caret <= 0) return null
  let i = caret - 1
  // Walk back from caret looking for '/'
  while (i >= 0) {
    const ch = value[i]!
    if (ch === '/') break
    if (!isSlugChar(ch)) return null
    i--
  }
  if (i < 0 || value[i] !== '/') return null
  // '/' must be at start or after a boundary
  const before = i > 0 ? value[i - 1]! : ''
  if (before && !isBoundaryChar(before)) return null
  return { start: i, filter: value.slice(i + 1, caret) }
}

function isSlugChar(ch: string) {
  return /[a-z0-9._-]/.test(ch)
}

function isBoundaryChar(ch: string) {
  // Whitespace or common punctuation.
  return /[\s({\[,;:!?'"`]/.test(ch)
}

function onInput(e: Event) {
  const el = e.target as HTMLInputElement
  emit('update:modelValue', el.value)
  refreshSlashState(el)
}

function refreshSlashState(el: HTMLInputElement) {
  const caret = el.selectionStart ?? 0
  const m = detectSlash(el.value, caret)
  if (!m) {
    if (open.value) closeDropdown()
    // If the value no longer starts with "/<slug>" at position 0,
    // the user deleted the slash — emit skill(null).
    if (selectedSlug.value && !valueStartsWithSlug(el.value)) {
      selectedSlug.value = null
      emit('skill', null)
    }
    return
  }
  triggerStart.value = m.start
  filter.value = m.filter
  if (!open.value && props.skills.length > 0) {
    open.value = true
    activeIndex.value = 0
  }
  void nextTick(() => positionPopover(el, m.start))
}

function valueStartsWithSlug(value: string): boolean {
  if (!selectedSlug.value) return false
  const prefix = '/' + selectedSlug.value
  return value.startsWith(prefix)
}

function positionPopover(el: HTMLInputElement, start: number) {
  // Use a hidden mirror span trick to get the pixel position of
  // the '/' character. For an <input> this is simpler than for a
  // <textarea> because the text is single-line.
  const style = window.getComputedStyle(el)
  const rect = el.getBoundingClientRect()
  // Approximate: the popover is placed below the input, with its left
  // edge aligned to the start of the slash. For an <input> we can
  // measure the text up to the slash via a hidden canvas or simply
  // position it below the input box, left-aligned.
  // Simpler fallback — position directly below the input.
  popoverLeft.value = rect.left
  popoverTop.value = rect.bottom + 4
  // Refined: create a hidden span to measure the pixel offset of the '/'.
  // This is the standard approach for input-based autocomplete.
  const mirror = ensureMirror()
  // Copy relevant styles
  mirror.style.fontFamily = style.fontFamily
  mirror.style.fontSize = style.fontSize
  mirror.style.fontWeight = style.fontWeight
  mirror.style.fontStyle = style.fontStyle
  mirror.style.letterSpacing = style.letterSpacing
  mirror.style.paddingLeft = style.paddingLeft
  mirror.style.paddingRight = style.paddingRight
  mirror.style.width = style.width
  mirror.textContent = el.value.slice(0, start)
  const span = document.createElement('span')
  span.textContent = '/'
  mirror.appendChild(span)
  const spanRect = span.getBoundingClientRect()
  popoverLeft.value = rect.left + (spanRect.left - parseFloat(style.paddingLeft || '0'))
  // Clean up mirror
  mirror.textContent = ''
}

let mirror: HTMLDivElement | null = null
function ensureMirror(): HTMLDivElement {
  if (mirror) return mirror
  mirror = document.createElement('div')
  mirror.style.position = 'fixed'
  mirror.style.top = '0'
  mirror.style.left = '0'
  mirror.style.visibility = 'hidden'
  mirror.style.whiteSpace = 'pre'
  mirror.style.pointerEvents = 'none'
  mirror.style.zIndex = '-1'
  document.body.appendChild(mirror)
  return mirror
}

function onKeydown(e: KeyboardEvent) {
  if (!open.value) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (filtered.value.length === 0) return
    activeIndex.value = (activeIndex.value + 1) % filtered.value.length
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (filtered.value.length === 0) return
    activeIndex.value =
      (activeIndex.value - 1 + filtered.value.length) % filtered.value.length
  } else if (e.key === 'Enter' || e.key === 'Tab') {
    const pick = filtered.value[activeIndex.value]
    if (pick) {
      e.preventDefault()
      acceptSuggestion(pick)
    }
  } else if (e.key === 'Escape') {
    e.preventDefault()
    closeDropdown()
    selectedSlug.value = null
    emit('skill', null)
  }
}

function acceptSuggestion(s: Skill) {
  const el = inputRef.value
  if (!el || triggerStart.value < 0) return
  const slugText = '/' + s.slug
  const after = props.modelValue.slice(el.selectionStart ?? 0)
  const supplement = after.replace(/^\s+/, '')
  const next = slugText + ' '
  emit('update:modelValue', next)
  selectedSlug.value = s.slug
  emit('skill', { slug: s.slug, supplement })
  closeDropdown()
  void nextTick(() => {
    if (!inputRef.value) return
    inputRef.value.focus()
    inputRef.value.setSelectionRange(next.length, next.length)
  })
}

onBeforeUnmount(() => {
  if (mirror) {
    mirror.remove()
    mirror = null
  }
})
</script>

<template>
  <div class="relative">
    <input
      ref="inputRef"
      :value="modelValue"
      :placeholder="placeholder"
      :disabled="disabled"
      :class="cn(
        'file:text-foreground placeholder:text-muted-foreground selection:bg-primary selection:text-primary-foreground dark:bg-input/30 border-input h-9 w-full min-w-0 rounded-md border bg-transparent px-3 py-1 text-base shadow-xs transition-[color,box-shadow] outline-none file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm',
        'focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]',
        'aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive',
      )"
      @input="onInput"
      @keydown="onKeydown"
      @click="refreshSlashState($event.target as HTMLInputElement)"
      @keyup="refreshSlashState($event.target as HTMLInputElement)"
      @blur="closeDropdown"
    />
    <!-- Skill badge: shown when a skill is selected -->
    <span
      v-if="selectedSlug"
      class="absolute right-2 top-1/2 -translate-y-1/2 inline-flex items-center gap-1 rounded bg-secondary px-2 py-0.5 text-xs font-medium text-secondary-foreground"
    >
      ✦ {{ selectedSlug }}
    </span>
    <Teleport to="body">
      <div
        v-if="open && filtered.length > 0"
        class="z-50 max-h-64 w-64 overflow-y-auto rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
        :style="{ position: 'fixed', top: `${popoverTop}px`, left: `${popoverLeft}px` }"
        @mousedown.prevent
      >
        <button
          v-for="(s, i) in filtered"
          :key="s.slug"
          type="button"
          class="flex w-full flex-col items-start rounded px-2 py-1.5 text-left text-sm hover:bg-accent hover:text-accent-foreground"
          :class="i === activeIndex ? 'bg-accent text-accent-foreground' : ''"
          @mouseenter="activeIndex = i"
          @mousedown.prevent="acceptSuggestion(s)"
        >
          <span class="font-medium">{{ s.name || s.slug }}</span>
          <span class="text-xs text-muted-foreground truncate max-w-full">{{ s.description }}</span>
        </button>
      </div>
      <div
        v-if="open && filtered.length === 0"
        class="z-50 w-64 rounded-md border bg-popover p-3 text-sm text-muted-foreground shadow-md"
        :style="{ position: 'fixed', top: `${popoverTop}px`, left: `${popoverLeft}px` }"
      >
        {{ $t('issue.skill.empty') }}
      </div>
    </Teleport>
  </div>
</template>
