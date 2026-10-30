/** Renders the saved-cost card as a 1080x1920 story image and shares or downloads it. */

export type StoryData = {
  saved_idr: number
  saved_pct: number
  baseline_idr: number
  model: string
  windowLabel: string
}

const W = 1080
const H = 1920
const FONT = '-apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif'

function rupiah(n: number): string {
  return 'Rp ' + Math.round(n).toLocaleString('en-US')
}

/** fit shrinks the font until the text fits the width. */
function fit(ctx: CanvasRenderingContext2D, text: string, weight: number, size: number, max: number): string {
  for (let s = size; s > 24; s -= 4) {
    const f = `${weight} ${s}px ${FONT}`
    ctx.font = f
    if (ctx.measureText(text).width <= max) return f
  }
  return `${weight} 24px ${FONT}`
}

export function renderStory(d: StoryData): Promise<Blob> {
  const c = document.createElement('canvas')
  c.width = W
  c.height = H
  const ctx = c.getContext('2d')
  if (!ctx) return Promise.reject(new Error('canvas is not available'))

  const bg = ctx.createLinearGradient(0, 0, 0, H)
  bg.addColorStop(0, '#14181f')
  bg.addColorStop(1, '#1a2d6d')
  ctx.fillStyle = bg
  ctx.fillRect(0, 0, W, H)

  ctx.textAlign = 'center'
  ctx.textBaseline = 'alphabetic'

  ctx.fillStyle = '#8fb5ff'
  ctx.font = `600 44px ${FONT}`
  ctx.fillText('XELLTER', W / 2, 330)

  ctx.fillStyle = '#c8ced9'
  ctx.font = `500 56px ${FONT}`
  ctx.fillText('Estimated cost saved', W / 2, 760)
  ctx.fillText(d.windowLabel.toLowerCase(), W / 2, 830)

  const positive = d.saved_idr >= 0
  ctx.fillStyle = positive ? '#12b76a' : '#f04438'
  const amount = rupiah(Math.abs(d.saved_idr))
  ctx.font = fit(ctx, amount, 800, 190, W - 160)
  ctx.fillText((positive ? '' : '-') + amount, W / 2, 1060)

  if (d.baseline_idr > 0) {
    ctx.fillStyle = '#ffffff'
    ctx.font = `600 64px ${FONT}`
    ctx.fillText(`${d.saved_pct.toFixed(0)}% ${positive ? 'cheaper' : 'more'}`, W / 2, 1170)
  }

  ctx.fillStyle = '#9aa3b2'
  ctx.font = `400 44px ${FONT}`
  ctx.fillText('than running the same traffic on', W / 2, 1300)
  ctx.fillStyle = '#ffffff'
  ctx.font = fit(ctx, d.model, 600, 56, W - 160)
  ctx.fillText(d.model, W / 2, 1375)

  ctx.fillStyle = '#6b7484'
  ctx.font = `400 34px ${FONT}`
  ctx.fillText('smart routing + RTK compression', W / 2, 1640)

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
