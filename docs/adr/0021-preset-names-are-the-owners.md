# ADR 0021 — A preset is named by the owner

Date: 2026-10-27

## Status

Accepted. Supersedes the fixed preset names of ADR 0017 and ADR 0019.

## Context

A preset is the model name a client asks for. Until now the name was fixed:
`text`, `image`, `video`, `tts`, `stt`, `music`, `systemone`, one per category.
The name was the category, so the owner could not say what a preset was *for*.
A client that wanted a fast model and a cheap model had to live with `text`.

The name was also the config key, which made the two inseparable: a rename was
a change of category.

## Decision

**A preset carries a name of the owner's own, and a category that says what it
serves.**

- The name is free-form: letters, digits, dot, dash and underscore, up to 63
  characters, unique inside a client.
- The category is a separate field: `chat`, `images`, `videos`,
  `audio_speech`, `audio_transcription`, `music`, `systemone`.
- A client still holds **at most one preset per category**. Renaming a preset
  does not create a second one.
- The name is the model name the client asks for. Renaming it means the app
  must ask for the new name.

The old fixed names keep their meaning, so a config written before this change
still loads: a preset called `text` with no category is a chat preset. The
panel suggests `text` for a new chat preset, but that is a suggestion.

## Consequences

- A client can now hold `fast-text` and `cheap-text`? No: it holds one text
  preset, named whatever the owner likes. The category is the limit, not the
  name.
- The panel gains a rename action on every preset card, and a name field in the
  "new preset" dialog.
- `config.PresetName` is now a suggestion, not a rule; `CategoryOfPreset` only
  recognises the old fixed names.
- Renaming a preset breaks the apps that ask for the old name. The panel says
  so in the rename dialog.
