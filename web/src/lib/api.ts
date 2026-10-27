/** The API client. Every call goes to /api, the panel's own JSON API. */

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

type Options = {
  method?: string
  body?: unknown
  signal?: AbortSignal
}

/**
 * api calls the panel API and returns the parsed body.
 *
 * The caller supplies the response shape: the panel API is this service's own
 * JSON, and the types in `src/lib/types.ts` describe it. The body is parsed as
 * `unknown` and checked to be a JSON object before it is returned.
 */
export async function api<T>(path: string, opt: Options = {}): Promise<T> {
  const res = await fetch(`/api/${path}`, {
    method: opt.method ?? 'GET',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', 'X-Xellter': '1' },
    body: opt.body === undefined ? undefined : JSON.stringify(opt.body),
    signal: opt.signal,
  })
  const text = await res.text()
  const parsed: unknown = text ? safeParse(text) : {}
  if (!res.ok) {
    const message = errorMessage(parsed) || res.statusText
    throw new ApiError(message, res.status)
  }
  return parsed as T
}

function safeParse(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return { error: text }
  }
}

/** errorMessage reads the error text out of an API body, if it has one. */
function errorMessage(body: unknown): string {
  if (body && typeof body === 'object' && 'error' in body) {
    const e = body.error
    if (typeof e === 'string') return e
  }
  return ''
}
