# book2audio — project conventions

Go CLI that turns a PDF into a Russian audiobook: `extract → chapters → translate → synth → build`. Go 1.27, one external dependency (`go.yaml.in/yaml/v3`).

## Commands

```sh
make build   # bin/book2audio
make test    # go test ./...
make vet
make race    # go test -race ./...
```

Run `gofmt -l .`, `go vet ./...` and `go test ./...` before every commit; run `make race` after touching `workspace`, `workpool`, `extract` or `pipeline`.

## Layout

| Package | Role |
|---|---|
| `cmd/book2audio` | `main`: signal handling, exit code |
| `internal/cli` | subcommand dispatch, shared flags, usage |
| `internal/config` | YAML config, defaults, validation |
| `internal/workspace` | working folder: layout, lock, manifest, hashes, atomic writes |
| `internal/execx` | `Runner` for external tools and its fake |
| `internal/workpool` | bounded worker pool |
| `internal/extract` | stage 1: page text, OCR fallback |
| `internal/clean` | headers, page numbers, paragraph assembly |
| `internal/chapters` | stage 2: heading detection, chapter files |
| `internal/chunk` | chapter parsing, chunking, sentence splitting |
| `internal/translate` | `Translator`, glossary, prompt, OpenAI-compatible client |
| `internal/speech` | text preparation and segmentation for speech |
| `internal/tts` | `Synthesizer`, `BatchSynthesizer`, Piper and Silero backends |
| `scripts/` | `silero_tts.py`, the engine side of the Silero backend |
| `internal/audio` | PCM WAV, joining, chapter metadata, ffmpeg encoding |
| `internal/pipeline` | stages 3–5 (`translate`, `synth`, `build`), `run`, `status` |
| `internal/testdata` | PDF fixtures (`gen.sh`), shared through `go:embed` |

## Rules

- **Language.** Everything is in English: code comments, commit messages, CLI output, errors, log messages, README and the example config. The only Russian strings are content of a Russian audiobook: `chapters.IntroTitle`, the fallback section titles and `speech.URLWord`; `chapters.Titles` and `speech.URLWordFor` choose them by `target_lang`.
- **Same language.** When `Config.SameLanguage()` holds, the translate stage uses `translate.Passthrough` and needs no model settings; get the translator from `Deps.Translator`, not from `NewTranslator` directly.
- **External tools** are run only through `execx.Runner`. Unit tests use `execx.FakeRunner`; tests with real tools skip themselves when the tool is missing.
- **Backends** are created by a factory (`translate.New`, `tts.New`) and injected through `pipeline.Deps`. Tests use `translate.FakeTranslator` and `tts.FakeSynthesizer`, which count their calls — assert on the calls to prove that work was *not* repeated.
- **Speech backends.** An engine that is expensive to start implements `tts.BatchSynthesizer`; the synth stage then gives each worker one batch instead of one segment. A per-segment error is returned in the slice and recorded as a failed unit; an engine that cannot run at all is a fatal error.
- **Paths.** `workspace.Open` makes the folder absolute; build every path with the `Workspace` path methods.
- **Files are written atomically** (`workspace.WriteFileAtomic`, or a `.part` file renamed on success). The translate and synth stages take an existing cache file for a finished unit, so a partial file must never appear at a cache path.
- **Units and hashes.** Every unit of work has an input hash; `Manifest.NeedsWork` decides whether to redo it. What goes into each hash:

  | Stage | Unit | Input hash |
  |---|---|---|
  | extract | page | PDF sha256, page number, `ocr_min_chars`, OCR language, `ocr_dpi` |
  | chapters | `index` (chapters.json) | all page texts, heading settings, `clean.RulesVersion`; the generated titles when they are not the Russian ones |
  | chapters | chapter | title, cleaned source lines, `clean.RulesVersion` |
  | translate | chunk (ID = hash) | chunk text, glossary terms that occur in it, backend, model, languages, temperature, `translate.PromptHash()`; in same-language mode only the chunk text |
  | translated | chapter file | ordered chunk hashes |
  | synth | segment (ID = hash) | segment text, backend, command, args before `{out}` is substituted; `accent_command` and its args when set |
  | build | chapter WAV | ordered segment hashes with paragraph flags, pause length |
  | build | output file | chapter hashes and titles, format, bitrate, tags |

  When a new setting affects a result, add it to the hash. When the cleaning rules change, bump `clean.RulesVersion`.
- **Hand edits.** `chapters.json`, `chNN.md`, `chNN.ru.md` and `glossary.yaml` belong to the user. Before overwriting a file of the first three kinds check `Manifest.Protected`; `--force` (`Manifest.Force`) is the only way to discard an edit.
- **A failed unit does not stop its stage**: record it with `Manifest.MarkFailed`, return `nil` from the worker, and return `workspace.ErrUnitsFailed` from the stage. Return an error from a worker only for fatal conditions; a cancelled context is not a unit failure.
- **Pruning.** At the end of a full run a stage calls `Manifest.Prune` with its current units. Cache files stay on disk.
- **Tests** are table-driven, cover success and error paths, and are written in the same commit as the code. Regenerate fixtures with `internal/testdata/gen.sh`, golden files with `go test ./internal/clean/ -update`.
