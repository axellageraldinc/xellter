/** The shapes the panel API returns. They mirror the Go structs in internal/admin. */

export type Purpose = 'general' | 'coding' | 'agentic' | 'vision' | 'bulk'
export type Optimization = 'cheapest' | 'balanced' | 'best_quality' | 'fastest'
export type Category =
  | 'chat'
  | 'images'
  | 'videos'
  | 'audio_speech'
  | 'audio_transcription'
  | 'music'
  | 'systemone'

export type Option = { value: string; label: string }

export type CategoryOption = { category: Category; preset: string; label: string; fixed: boolean }

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
  provider: string
}

/** ProviderOption is one upstream; the panel offers a provider choice only when there are two or more. */
export type ProviderOption = { id: string; type: string; base_url: string; models: number; keys: number }

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
  free_models: string[]
  /** Free or discounted models xellter would add, that the owner unticked. */
  excluded_models: string[]
  /** Everything the owner unticked, including models that are not free right now. */
  excluded: string[]
  /** A session keeps one model for its whole life. */
  sticky: boolean
  ranking: Ranking | null
  evictions: Record<string, string>
  /** The models that are out of this preset, and why. */
  out_of_service: OutOfService[]
  /** How each model did in this preset over the last 24 hours. */
  stats: Record<string, ModelStat>
  /** Who was taken out of this preset, newest first. */
  history: ModelEvent[]
}

export type ModelStat = {
  model: string
  requests: number
  failed: number
  fail_pct: number
  /** Median wait for the first token of the streamed answers, in ms; 0 = no data. */
  ttft_p50_ms: number
  ttft_p90_ms: number
}

export type ModelEvent = {
  /** Unix milliseconds. */
  ts: number
  model: string
  kind: string
  reason: string
  /** Unix milliseconds. */
  until: number
  strikes: number
}

export type OutOfService = {
  model: string
  reason: string
  /** errors | slow | unreliable */
  kind: string
  since: string
  until: string
  /** How many times in a row it came back and was put out again. */
  strikes: number
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
  providers: ProviderOption[]
  computed_at: string
  routing: {
    rank_refresh_minutes: number
    top_models: number
    evict_errors: number
    evict_window_minutes: number
    evict_cooldown_minutes: number
    avoid_sunset_days: number
  }
}

/** CatalogMix is the client's own average chat request, from its last 7 days. */
export type CatalogMix = { known: boolean; requests: number; avg_in_tok?: number; avg_out_tok?: number; cached_share?: number }

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
  provider: string
  mix_cost_idr: number
  mix_cost_known: boolean
  reasoning: boolean
  effort_levels?: string[]
  effort_toggle?: boolean
  effort_refused?: string[]
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
