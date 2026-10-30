/** Renders the saved-cost card as a 1080x1920 story image and shares or downloads it. */

export type StoryRow = {
  model: string
  baseline_idr: number
  saved_idr: number
  saved_pct: number
}

export type StoryData = {
  /** up to three comparisons; with more than one the hero is their combined saving */
  rows: StoryRow[]
  windowLabel: string
}

const W = 1080
const H = 1920
const FONT = '-apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'
const GREEN = '#12b76a'
const RED = '#f04438'
const CARD_H = 170
const CARD_GAP = 24

function rupiah(n: number): string {
  return 'Rp ' + Math.round(n).toLocaleString('en-US')
}

/** fit shrinks the font until the text fits the width. */
function fit(ctx: CanvasRenderingContext2D, text: string, weight: number, size: number, max: number): string {
  for (let s = size; s > 20; s -= 2) {
    const f = `${weight} ${s}px ${FONT}`
    ctx.font = f
    if (ctx.measureText(text).width <= max) return f
  }
  return `${weight} 20px ${FONT}`
}

function glow(ctx: CanvasRenderingContext2D, x: number, y: number, r: number, color: string) {
  const g = ctx.createRadialGradient(x, y, 0, x, y, r)
  g.addColorStop(0, color)
  g.addColorStop(1, 'rgba(0,0,0,0)')
  ctx.fillStyle = g
  ctx.fillRect(x - r, y - r, r * 2, r * 2)
}

function pill(ctx: CanvasRenderingContext2D, cx: number, y: number, w: number, h: number, fill: string, stroke?: string) {
  ctx.beginPath()
  ctx.roundRect(cx - w / 2, y, w, h, h / 2)
  ctx.fillStyle = fill
  ctx.fill()
  if (stroke) {
    ctx.strokeStyle = stroke
    ctx.lineWidth = 2
    ctx.stroke()
  }
}

function background(ctx: CanvasRenderingContext2D) {
  const bg = ctx.createLinearGradient(0, 0, W * 0.4, H)
  bg.addColorStop(0, '#0d1015')
  bg.addColorStop(0.55, '#14214f')
  bg.addColorStop(1, '#0d1015')
  ctx.fillStyle = bg
  ctx.fillRect(0, 0, W, H)

  glow(ctx, 900, 380, 640, 'rgba(54,101,240,0.55)')
  glow(ctx, 120, 1150, 700, 'rgba(18,183,106,0.30)')
  glow(ctx, 1000, 1750, 560, 'rgba(91,141,251,0.30)')

  // dot grid, fading toward the bottom
  for (let y = 60; y < H; y += 60) {
    for (let x = 60; x < W; x += 60) {
      ctx.fillStyle = `rgba(255,255,255,${0.07 * (1 - y / H)})`
      ctx.beginPath()
      ctx.arc(x, y, 2.5, 0, Math.PI * 2)
      ctx.fill()
    }
  }

  // concentric rings behind the hero number
  ctx.strokeStyle = 'rgba(255,255,255,0.05)'
  ctx.lineWidth = 2
  for (let r = 260; r <= 860; r += 150) {
    ctx.beginPath()
    ctx.arc(W / 2, 700, r, 0, Math.PI * 2)
    ctx.stroke()
  }
}

function card(ctx: CanvasRenderingContext2D, r: StoryRow, y: number) {
  const x = 80
  const w = W - 160
  const ok = r.saved_idr >= 0
  const tone = ok ? '#32d583' : '#f97066'

  ctx.beginPath()
  ctx.roundRect(x, y, w, CARD_H, 36)
  ctx.fillStyle = 'rgba(255,255,255,0.07)'
  ctx.fill()
  ctx.strokeStyle = 'rgba(255,255,255,0.13)'
  ctx.lineWidth = 2
  ctx.stroke()

  ctx.textAlign = 'left'
  ctx.fillStyle = '#ffffff'
  ctx.font = fit(ctx, r.model, 600, 40, 520)
  ctx.fillText(r.model, x + 40, y + 62)
  ctx.fillStyle = '#9aa3b2'
  ctx.font = `400 28px ${FONT}`
  ctx.fillText(`would cost ${rupiah(r.baseline_idr)}  ·  you paid ${rupiah(r.baseline_idr - r.saved_idr)}`, x + 40, y + 108)

  ctx.textAlign = 'right'
  ctx.fillStyle = tone
  ctx.font = fit(ctx, (ok ? '' : '-') + rupiah(Math.abs(r.saved_idr)), 800, 46, 330)
  ctx.fillText((ok ? '' : '-') + rupiah(Math.abs(r.saved_idr)), x + w - 40, y + 66)
  if (r.baseline_idr > 0) {
    ctx.fillStyle = '#c8ced9'
    ctx.font = `500 28px ${FONT}`
    ctx.fillText(`${r.saved_pct.toFixed(0)}% ${ok ? 'saved' : 'more'}`, x + w - 40, y + 108)
  }

  // savings bar
  const bx = x + 40
  const bw = w - 80
  ctx.beginPath()
  ctx.roundRect(bx, y + 136, bw, 8, 4)
  ctx.fillStyle = 'rgba(255,255,255,0.10)'
  ctx.fill()
  const frac = r.baseline_idr > 0 ? Math.max(0, Math.min(1, r.saved_idr / r.baseline_idr)) : 0
  if (frac > 0) {
    ctx.beginPath()
    ctx.roundRect(bx, y + 136, Math.max(8, bw * frac), 8, 4)
    ctx.fillStyle = tone
    ctx.fill()
  }
}

export function renderStory(d: StoryData): Promise<Blob> {
  const c = document.createElement('canvas')
  c.width = W
  c.height = H
  const ctx = c.getContext('2d')
  const rows = d.rows.slice(0, 3)
  if (!ctx || rows.length === 0) return Promise.reject(new Error('nothing to draw'))
  const multi = rows.length > 1
  const hero: StoryRow = multi
    ? {
        model: `${rows.length} models`,
        baseline_idr: rows.reduce((a, r) => a + r.baseline_idr, 0),
        saved_idr: rows.reduce((a, r) => a + r.saved_idr, 0),
        saved_pct: 0,
      }
    : rows[0]

  background(ctx)
  ctx.textBaseline = 'alphabetic'

  // brand: small, in the corner of attention, below the top strip Instagram covers
  ctx.textAlign = 'center'
  pill(ctx, W / 2, 250, 260, 68, 'rgba(255,255,255,0.08)', 'rgba(255,255,255,0.16)')
  ctx.fillStyle = GREEN
  ctx.beginPath()
  ctx.arc(W / 2 - 78, 284, 9, 0, Math.PI * 2)
  ctx.fill()
  ctx.fillStyle = '#ffffff'
  ctx.font = `700 34px ${FONT}`
  ctx.textAlign = 'left'
  ctx.fillText('xellter', W / 2 - 52, 296)

  // the block below the brand is centred when fewer than three cards are shown
  const shift = ((3 - rows.length) * (CARD_H + CARD_GAP)) / 2

  ctx.textAlign = 'center'
  ctx.fillStyle = '#c8ced9'
  ctx.font = `500 54px ${FONT}`
  ctx.fillText(multi ? 'Combined cost saved' : 'Estimated cost saved', W / 2, 520 + shift)
  ctx.fillStyle = '#8fb5ff'
  ctx.font = `600 34px ${FONT}`
  ctx.fillText(d.windowLabel.toUpperCase(), W / 2, 580 + shift)

  const ok = hero.saved_idr >= 0
  const amount = (ok ? '' : '-') + rupiah(Math.abs(hero.saved_idr))
  ctx.font = fit(ctx, amount, 800, 200, W - 140)
  const hy = 790 + shift
  const grad = ctx.createLinearGradient(0, hy - 160, 0, hy)
  grad.addColorStop(0, ok ? '#a6f4c5' : '#fda29b')
  grad.addColorStop(1, ok ? GREEN : RED)
  ctx.shadowColor = ok ? 'rgba(18,183,106,0.5)' : 'rgba(240,68,56,0.5)'
  ctx.shadowBlur = 50
  ctx.fillStyle = grad
  ctx.fillText(amount, W / 2, hy)
  ctx.shadowBlur = 0

  if (multi || hero.baseline_idr > 0) {
    const label = multi
      ? `added up over ${rows.length} comparisons`
      : `${hero.saved_pct.toFixed(0)}% ${ok ? 'cheaper' : 'more'} than ${hero.model}`
    ctx.font = fit(ctx, label, 600, 38, W - 220)
    const w = Math.min(W - 120, ctx.measureText(label).width + 80)
    pill(ctx, W / 2, hy + 50, w, 74, ok ? 'rgba(18,183,106,0.18)' : 'rgba(240,68,56,0.18)', ok ? 'rgba(50,213,131,0.5)' : 'rgba(249,112,102,0.5)')
    ctx.fillStyle = '#ffffff'
    ctx.fillText(label, W / 2, hy + 100)
  }

  rows.forEach((r, i) => card(ctx, r, 1010 + shift + i * (CARD_H + CARD_GAP)))

  ctx.textAlign = 'center'
  ctx.fillStyle = '#6b7484'
  ctx.font = `400 30px ${FONT}`
  ctx.fillText('smart model routing', W / 2, 1650)

  return new Promise((resolve, reject) =>
    c.toBlob((b) => (b ? resolve(b) : reject(new Error('could not render the image'))), 'image/png'),
  )
}

/** shareStory opens the share sheet where files are supported (Instagram is in it on phones), else downloads. */
export async function shareStory(d: StoryData): Promise<'shared' | 'downloaded' | 'cancelled'> {
  const blob = await renderStory(d)
  const file = new File([blob], 'xellter-saved-cost.png', { type: 'image/png' })
  if (navigator.canShare?.({ files: [file] })) {
    try {
      await navigator.share({ files: [file] })
      return 'shared'
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return 'cancelled'
    }
  }
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = file.name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
  return 'downloaded'
}
