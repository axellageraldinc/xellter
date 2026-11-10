# 0048 — Transcription priced by audio length; balance history

## Status
Accepted

## Context
A transcription upload is multipart, so the proxy saw no JSON fields and priced it at 0 units (Rp0). The log also holds only catalog prices, never what kenari took from the balance.

## Decision
- The proxy reads the audio length from the container header (WAV, FLAC, Ogg Opus/Vorbis, MP4/M4A, MP3, WebM with a Duration) and bills `seconds x price`. When the length cannot be told (a browser WebM without Duration), a nominal minute is billed. Nothing is decoded.
- Every balance reading (the 5-minute account refresh) is stored in `balance_log`, kept 100 days. The Usage page sums the drops over the window and shows it beside the catalog price of the pay-as-you-go calls. It is account-wide, not per client or call. A top-up between two readings hides the spend in that gap, so the figure is a floor.

## Consequences
Per-call cost stays at catalog price; the real deduction is visible per window only.
