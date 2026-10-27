/** The shapes the panel API returns. They mirror the Go structs in internal/admin. */

export type Purpose = 'general' | 'coding' | 'agentic' | 'vision' | 'bulk'
export type Optimization = 'cheapest' | 'balanced' | 'best_quality'
export type Category =
  | 'chat'
  | 'images'
  | 'videos'
  | 'audio_speech'
  | 'audio_transcription'
  | 'music'
  | 'systemone'

export type Option = { value: string; label: string }

export type CategoryOption = { category: Category; preset: string; label: string }

export type KeyRow = {
  name: string
  hint: string
  has_secret: boolean
  probed: boolean
  unrestricted: boolean
  allowed: number
  blocked: number
  cooldown_until: string
  breaker_until: string
  last_error: string
  last_ok: string
  tier: string
  state: string
  capability: string
}

export type Candidate = {
  model: string
  score: number
  cost_idr: number
  list_cost_idr: number
  billing: string
  bench: number
  bench_source: string
  flags: string[]
  reason: string
}

export type Rejected = { model: string; why: string }

export type Ranking = {
  client: string
  preset: string
  category: Category
  optimization: Optimization
  purpose: Purpose
  candidates: Candidate[]
  rejected: Rejected[]
  unranked: string[]
  reserve: Candidate[]
  computed: string
}

export type PresetView = {
  name: string
  category: Category
  label: string
  optimization: Optimization
  optimization_label: string
  models: string[]
  auto_models: string[]
  ranking: Ranking | null
  evictions: Record<string, string>
}

export type PresetRow = {
  name: string
  category: Category
  label: string
  optimization: Optimization
  optimization_label: string
  pool: number
  serving: number
  top: string
  rejected: number
  unranked: number
}

export type ClientRow = {
  name: string
  enabled: boolean
  purpose: Purpose
  purpose_label: string
  key_hint: string
  note: string
  keys: KeyRow[]
  presets: PresetRow[]
  requests_24h: number
  errors_24h: number
  cost_24h: number
  rtk_saved_24h: number
}

export type Alert = { level: 'info' | 'warn' | 'bad'; text: string; client?: string }

export type PlanWindow = {
  budget_rp: number
  used_rp: number
  remaining_rp: number
  used_frac: number
  resets_at: string
}

export type Account = {
  name?: string
  tier?: string
  plan?: string
  plan_source?: string
  quota_key?: string
  /** The plan windows, keyed by their name: `week`, `month`. */
  windows?: Record<string, PlanWindow>
  estimated_spend?: number[]
  plan_budget?: number[]
  balance_idr?: number
}

export type Overview = {
  status: 'ok' | 'warn' | 'bad'
  uptime_s: number
  alerts: Alert[]
  accounts: Account[]
  account_key: { configured: boolean }
  catalog?: { models: number; degraded: boolean }
  clients: ClientRow[]
}

export type ClientDetail = {
  client: { name: string; enabled: boolean; purpose: Purpose; key_hint: string; note: string }
  row: ClientRow
  keys: KeyRow[]
  presets: PresetView[]
  purposes: Option[]
  optimizations: Option[]
  categories: CategoryOption[]
  computed_at: string
  routing: {
    rank_refresh_minutes: number
    top_models: number
    evict_errors: number
    evict_window_minutes: number
    evict_cooldown_minutes: number
    avoid_sunset_days: number
    sticky_session: boolean
  }
}

export type CatalogRow = {
  id: string
  owner: string
  endpoint: string
  context: number
  modalities: string
  tool_call: boolean
  free: boolean
  billing: string
  callable: boolean
  in_idr: number
  out_idr: number
  cache_read_idr: number
  cache_write_idr: number
  has_cache_write: boolean
  lines?: { unit: string; variant: string; idr: number }[]
  ref_cost_idr: number
  ref_cost_known: boolean
  promo_off: number
  promo_ends?: string
  sunset_at?: string
  subscriber_only: boolean
}

export type UsageRow = {
  label: string
  key: string
  client: string
  preset: string
  requests: number
  errors: number
  in_tok: number
  out_tok: number
  cached_tok: number
  rtk_saved: number
  cost_idr: number
  billing?: string
  /** The client is no longer in the configuration; this is its old traffic. */
  removed?: boolean
}

export type UsageSeriesPoint = {
  bucket: string
  requests: number
  errors: number
  cost_idr: number
  rtk_saved: number
}

export type Usage = {
  level: 'client' | 'preset' | 'model'
  rows: UsageRow[]
  totals: {
    requests?: number
    errors?: number
    in_tok?: number
    out_tok?: number
    rtk_saved?: number
    cost_idr?: number
  }
  series: UsageSeriesPoint[]
  kenari?: {
    configured: boolean
    account?: { plan?: string; plan_source?: string; balance_idr?: number }
    windows?: { label: string; used_idr: number; budget_idr: number; resets_at: string }[]
  }
}

export type ConfigFile = {
  config: Record<string, unknown>
  yaml: string
  history: string[]
}

export type SessionInfo = {
  session: {
    preset?: string
    requests: number
    errors: number
    in_tok: number
    out_tok: number
    cached_tok: number
    cost_idr: number
    models?: string[]
  }
  pins: Record<string, { model: string; live_for_s: number }>
}
