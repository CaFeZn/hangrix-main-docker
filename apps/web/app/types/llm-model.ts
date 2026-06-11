export interface LLMModelListItem {
  id: number
  name: string
  display_name: string
  context_window: number
  max_output_tokens: number
  vision: boolean
  member_count: number
  available_count: number
  created_at: string
  updated_at: string
}

export interface LLMModelListResp {
  items: LLMModelListItem[]
}

export interface LLMModelMember {
  id: number
  provider_id: number
  provider_name: string
  model: string
  priority: number
  health: string
  manual_disabled: boolean
  auto_disabled_until: string | null
  recover_in_seconds: number
  backoff_step: number
  last_failure_at: string | null
  last_failure_msg: string
  last_success_at: string | null
  last_checked_at: string | null
}

export interface LLMModel {
  id: number
  name: string
  display_name: string
  context_window: number
  max_output_tokens: number
  vision: boolean
  reasoning_effort_map: Record<string, string>
  group_id: number
  members: LLMModelMember[]
  actor_id: number
  created_at: string
  updated_at: string
}

export interface LLMModelCreateMember {
  provider_id: number
  model: string
}

export interface LLMModelCreateReq {
  name: string
  display_name: string
  context_window: number
  max_output_tokens: number
  vision: boolean
  reasoning_effort_map: Record<string, string>
  members: LLMModelCreateMember[]
}

export interface LLMModelPatchReq {
  display_name?: string
  context_window?: number
  max_output_tokens?: number
  vision?: boolean
  reasoning_effort_map?: Record<string, string>
  members?: LLMModelCreateMember[]
}
