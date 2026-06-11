export type EntryStatus = 'available' | 'auto_disabled' | 'manual_disabled'

export interface ModelGroupEntry {
  id: number
  model_name: string
  priority: number
  status: EntryStatus
  consecutive_failures: number
  disabled_until: string | null
  last_checked_at: string | null
  remaining_seconds?: number
}

export interface ModelGroup {
  id: number
  name: string
  entries: ModelGroupEntry[]
  created_at: string
  updated_at: string
}

export interface ModelGroupListItem {
  id: number
  name: string
  entry_count: number
  available_count: number
  created_at: string
}

export interface ModelGroupListResp {
  items: ModelGroupListItem[]
}

export interface ModelGroupCreateEntry {
  model_name: string
  priority: number
}

export interface ModelGroupCreateReq {
  name: string
  entries: ModelGroupCreateEntry[]
}

export interface ModelGroupUpdateReq {
  entries: ModelGroupCreateEntry[]
}
