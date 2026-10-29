package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/axellageraldinc/xellter/internal/spend"
)

// alertRepeat is how long a client that keeps spiking is left alone after its webhook was sent.
const alertRepeat = 6 * time.Hour

// SpendSpikes lists the clients whose last hour cost far more than their usual hour (ADR 0041). The cost is the
// request log's, at list price. Switched off by a factor of zero or less.
func (a *App) SpendSpikes(now time.Time) []spend.Spike {
	al := a.Cfg().Alerts
	if al.SpikeFactor <= 0 {
		return nil
	}
	cost := func(since time.Time) map[string]float64 {
		out := map[string]float64{}
		rows, err := a.St.Usage("client", since, "", "")
		if err != nil {
			return out
		}
		for _, r := range rows {
			out[r.Client] = r.Cost
		}
		return out
	}
	return spend.Find(cost(now.Add(-time.Hour)), cost(now.Add(-time.Duration(spend.Window)*time.Hour)), al.SpikeFactor, al.SpikeMinIDR)
}

// SpikeText is the sentence the panel and the webhook show for a spike.
func SpikeText(s spend.Spike) string {
	if s.UsualIDR < 1 {
		return fmt.Sprintf("%s spent Rp %.0f in the last hour and has no history to compare with.", s.Client, s.HourIDR)
	}
	return fmt.Sprintf("%s spent Rp %.0f in the last hour, %.0f times its usual Rp %.0f an hour.", s.Client, s.HourIDR, s.HourIDR/s.UsualIDR, s.UsualIDR)
}

// NotifySpikes sends each new spike to the webhook, once per client per alertRepeat. The body is metadata only:
// the client's name and two rupiah figures, never a prompt. A failed POST is logged and tried again next time.
func (a *App) NotifySpikes(ctx context.Context, now time.Time) {
	url := a.Cfg().Alerts.WebhookURL
	if url == "" {
		return
	}
	for _, s := range a.SpendSpikes(now) {
		a.alertMu.Lock()
		last := a.alerted[s.Client]
		a.alertMu.Unlock()
		if now.Sub(last) < alertRepeat {
			continue
		}
		body, _ := json.Marshal(map[string]any{
			"text": "xellter: " + SpikeText(s), "client": s.Client, "hour_idr": s.HourIDR, "usual_hour_idr": s.UsualIDR,
		})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
		if err != nil {
			a.Logf("spend alert for %s not delivered: %v", s.Client, err)
			continue
		}
		a.alertMu.Lock()
		a.alerted[s.Client] = now
		a.alertMu.Unlock()
	}
}
