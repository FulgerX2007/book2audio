# book2audio

A command-line tool written in Go: it turns a PDF or an EPUB into a Russian audiobook split by chapters.

```
PDF or EPUB → text (OCR for scanned PDF pages) → chapters → translation → speech → M4B or MP3
```

It is meant for texts you have the rights to: your own documents and books in the public domain.

## Requirements

- Go 1.27 to build.
- `poppler-utils` (`pdfinfo`, `pdftotext`, `pdftoppm`) for text extraction from a PDF. An EPUB needs no extra tools.
- `tesseract-ocr` with the data of the book's language, only for pages without a text layer.
- `ffmpeg` with the `aac` and `libmp3lame` encoders to build the audiobook.
- A translation server with an OpenAI-compatible API: local (Ollama, llama.cpp, vLLM) or in the cloud.
- A speech engine: Silero (`pip install silero scipy`) or Piper. A Piper voice for the language is downloaded when it is missing.

## Build

```sh
make build        # the bin/book2audio binary
make test         # tests
make race         # tests with the race detector
```

Tests that need external tools skip themselves when the tool is not installed.

## Quick start

```sh
cp config.example.yaml config.yaml
# set translate.model in config.yaml

book2audio init book.pdf --config config.yaml
book2audio run book.pdf
```

The result is `work/book/out/book.m4b`. `run` can be interrupted and started again: finished work is not repeated.

## Commands

```
book2audio init <book.pdf> [--config cfg.yaml] [--workdir work] [--slug name]
book2audio <extract|chapters|translate|synth|build|status> <book.pdf | work/<slug>>
book2audio run <book.pdf | work/<slug>> [--from stage] [--to stage]
book2audio ui [--workdir work] [--addr 127.0.0.1:8080]
```

| Command | What it does |
|---|---|
| `init` | creates the working folder of the book and copies the config into it |
| `extract` | text of every page; pages without text go through OCR |
| `chapters` | removes running headers and page numbers, splits the text into chapters |
| `translate` | translates the chapters chunk by chunk; `--chapter N` does one chapter |
| `synth` | synthesises the translated chapters; `--chapter N` does one chapter |
| `build` | joins the chapter audio and encodes the audiobook |
| `run` | all stages in order; `--from` and `--to` limit them |
| `status` | progress by stage and by chapter, failures, files edited by hand |
| `ui` | the same in a browser, with buttons to add a book, run and stop |

Common flags: `--config` (a config for one run), `--workdir`, `--force`, `-v`. Flags may be written before or after the book path.

One process works on a book at a time: a second run on the same folder fails. The exception is `status`: it only reads, so it works while the book is being processed in another window. For each chapter it shows how many chunks are translated and how many segments are synthesised out of the total, for example `7/12`. Translation and speech do not go page by page: chapters are cut into chunks of `translate.chunk_chars` characters, so progress is counted in chunks.

## EPUB books

Give an `.epub` file wherever a PDF is expected: `book2audio init book.epub`, `book2audio run book.epub`. An EPUB is easier to read than a PDF, so the result is usually cleaner:

- the chapters come from the book's table of contents, with its titles; `chapters.heading_regex` and the other `chapters` settings are not used;
- paragraphs are taken as they are in the book: nothing is guessed from line lengths, and there are no page numbers or running headers to remove;
- footnote marks, images and navigation pages are left out.

A "page" of an EPUB is one document of its reading order: `01-pages/p0001.txt` holds the first one, with a paragraph per line. A document that begins a chapter has `# Title` as its first line. A document that is not in the table of contents continues the chapter before it; text ahead of the first chapter becomes chapter 0. `chapters.json` can be edited as for a PDF. Books protected by DRM cannot be read.

## Browser interface

```sh
book2audio ui --workdir ../books
```

It prints an address, by default `http://127.0.0.1:8080`; open it in a browser. From WSL the same address works in a browser on Windows. The page shows:

- the books of the directory, with the ones being processed marked;
- a form to add a book: upload a PDF or an EPUB, or give its path on the computer;
- for a book: progress by stage and by chapter, failures, files edited by hand, the log of the last run and the finished audiobook to download;
- **Run** and **Stop**, with the first and the last stage to run. A stopped run keeps its finished work, like Ctrl-C;
- **Settings** of the book: whether to translate it, the two languages, the model, the address of its API and the name of the variable that holds the API key; and the speech engine (Piper or Silero) with its command and arguments. With translation off the book is read in its own language and no model is needed. The page tells whether the key variable has a value; the key itself is never shown or stored.

`--config` names the config that is copied into books added from the page; `--addr` changes the address. Saving the settings rewrites only those keys of the book's `config.yaml`; comments and other keys are kept. The other settings, the glossary and the chapter texts are edited as files.

The interface has no password. It listens on this computer only and refuses requests that do not come from its own page. If you choose an address that other computers can reach (`--addr 0.0.0.0:8080`), anyone on that network can start runs and download the audio.

## Working folder

```
work/<slug>/
  config.yaml                         the book's main config
  glossary.yaml                     ✎ how to translate names and terms
  manifest.json                       state of the stages
  01-pages/p0001.txt …                page text
  02-chapters/chapters.json         ✎ where each chapter starts
  02-chapters/ch01.md …             ✎ chapter text in the source language
  03-translated/cache/                chunk translations
  03-translated/ch01.ru.md …        ✎ chapter translation
  04-audio/cache/                     synthesised segments
  04-audio/ch01.wav …                 chapter audio
  out/                                the finished audiobook
```

Files marked ✎ may be edited by hand.

## Hand edits

A file edited by hand is not overwritten. `status` lists such files; `--force` rebuilds them from the source data.

| What you changed | What is redone on the next run |
|---|---|
| `chapters.json` | chapters are rebuilt from the new starts |
| `chNN.md` | translation from the edit to the end of that chapter |
| `chNN.ru.md` | speech for the changed paragraphs only |
| `glossary.yaml` | translation of the chunks that mention the changed term |
| model, prompt, languages | the whole translation |
| voice (`tts.args`), stress command | all speech |
| `audio.bitrate`, `audio.format`, tags | encoding only |

Undoing an edit costs nothing: earlier translations and audio stay in the cache.

`chapters.json` gives the start of a chapter as a page and a line number in `01-pages/pNNNN.txt`; a chapter ends where the next one starts. Text before the first heading becomes chapter 0.

When a chunk cannot be translated or a segment cannot be synthesised, `status` shows the path where a result can be supplied by hand. The next run accepts it as done.

When a chunk translation is rejected by the check (empty, or much shorter or longer than the source), the model's answer is saved next to the cache as `<hash>.rejected.txt`; the log gives its path. If the answer is in fact good, rename the file to `<hash>.ru.txt`.

## Speech engines

| `tts.backend` | How it works | Stress |
|---|---|---|
| `silero` | `scripts/silero_tts.py` loads the model once per batch of segments; there are as many batches as `tts.workers` | placed by the engine, homographs included |
| `piper` | the command from `tts.command` runs for every segment | ignores context; needs `tts.accent_command` |

For Silero, in the book's config:

```yaml
tts:
  backend: silero
  command: /full/path/to/scripts/silero_tts.py
  args: ["--speaker", "xenia"]
  segment_chars: 800
```

The script accepts `--speaker`, `--model`, `--sample-rate` and `--device` (`cpu` or `cuda`). Its first line must name the interpreter of the environment where `silero` is installed; alternatively put the interpreter in `tts.command` and the script path first in `tts.args`.

## Piper voices

With `tts.backend: piper` and no `tts.args`, the synth stage gets the voice itself:

- the voice is `tts.voice`, or the default one for `target_lang`: `ru`, `en`, `de`, `fr` and `es` have a default; for any other language set `tts.voice` to a name from the [Piper voice list](https://huggingface.co/rhasspy/piper-voices), for example `it_IT-paola-medium`;
- its two files (`.onnx` and `.onnx.json`, about 60 MB) are downloaded once into `~/.cache/book2audio/voices` (or `tts.voices_dir`) and reused by every book;
- the engine arguments are built from the downloaded model.

Hand-written `tts.args` always win: nothing is downloaded when they are set. Only voice files are downloaded; the programs themselves (`piper`, `ffmpeg`, `tesseract` and its language data) are installed by you.

## Stress marks

Piper chooses the stress without looking at the context and gets homographs wrong. When `tts.accent_command` is set, the text passes through that command before speech:

- it receives one segment per line on standard input;
- it must print the same number of lines, in the same order, with stress marks added;
- it runs once for all segments of a run, so its model is loaded once.

Piper reads the stress mark U+0301 after the stressed vowel: `го́лоса`. Turning the command on or off, or changing it, re-synthesises the book; audio made with the earlier setting stays in the cache.

## A book in its own language

To make an audiobook without translating, set both languages to the same value:

```yaml
source_lang: en
target_lang: en
```

The translate stage then calls no model: it passes the chapter text on unchanged, so `translate.model` and an API key are not needed. Use a speech engine that reads that language, for example Piper with an English voice; Silero as set up by `scripts/silero_tts.py` reads only Russian. The chapter files of that stage keep their names (`chNN.ru.md`) and can still be edited by hand before speech.

## Language of the output

The tool's messages, log and errors are in English. Text that the tool adds to the audiobook follows `target_lang`: for Russian it is "Вступление" (the title of chapter 0), "Часть N" (fallback sections) and "ссылка" (read in place of a URL); for any other language it is "Introduction", "Part N" and "link". Changing `target_lang` of an existing book rebuilds its chapters.

## Limitations

- Paragraphs are restored from the shape of the lines: a paragraph ends on a short line that ends a sentence. If the last line of a paragraph is full width, it is joined to the next paragraph.
- Table of contents pages are left out of the text and the audio.
- Chapters are found with `chapters.heading_regex` (by default `Chapter N`, `Глава N`). If the book's headings look different, for example all capitals, set your own pattern: otherwise the text is split into sections of `fallback_pages` pages.
- Stress is placed by the speech engine; Piper may misread homographs ("го́лоса" and "голоса́"). An external stress command fixes that, see above.
- Piper starts a separate process for every segment; Silero starts once per batch.
- Silero reads only Russian text. Words in Latin letters (names, titles, terms left in English) are rewritten in Cyrillic by spelling rules before speech: "London" is read as "лондон", "FBI" as "эф би ай". The pronunciation is approximate; to fix a word, write it in Cyrillic in `chNN.ru.md`. A segment with nothing to read (only digits or dashes) becomes a short silence instead of a failure. Symbols the model does not know (such as "^") are dropped.
- The working folder lock works on Linux and macOS.
