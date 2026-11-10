# 0049 — Adding free and promo models is a per-preset toggle

Status: accepted

## Context
ADR 0019 adds every free model and every model on promo to the pool of each preset of that category. A preset
with one deliberate model (a brain, a judge) may not want any other model answering in its place.

## Decision
`Preset.auto_add` (bool, absent means true) switches the automatic additions for that preset. Off, only
`models` is routed; `excluded` is kept untouched, so turning it back on restores the earlier choices. Every
existing preset keeps adding, since an absent key means on. The panel shows it as a switch on the preset card.

## Consequences
- With it off and one model, the preset has no fallback (the last model is never evicted, ADR 0020).
