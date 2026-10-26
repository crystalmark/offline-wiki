# Offline Wiki

Ask questions in plain English and get cited answers from offline copies of Wikipedia and 40+ other reference libraries, with no internet needed. Everything lives on one USB drive and runs on Mac, Windows and Linux.

It's a "prepper" project: a self-contained reference library that keeps working when the internet doesn't.

## What it does

- **Chat with the library.** Questions are answered by a local language model that reads the relevant articles and cites them with numbered links. The links open the full offline article, with its images.
- **Runs from the drive.** Plug the drive into a computer and double-click a launcher; nothing is installed.
- **Safe to unplug.** On a Mac the drive is switched to read-only while in use, so it can be pulled out at any time. On other systems Offline Wiki still never writes to the drive while answering.
- **Keeps itself current.** An Update button (or script) downloads newer libraries, programs and models, verifying checksums before replacing anything. A Mac can update the drive automatically each week when it's plugged in.
- **Fits the computer.** It picks the best model for the available memory, and falls back to showing the relevant passages when there isn't enough memory for one.
- **Works on a phone.** With `--lan`, phones and other computers on the same network can use it in a browser.

## How it works

```
question ─► search terms ─► Kiwix full-text + title search across every library
         ─► fetch articles (following Wikipedia redirects) ─► split into passages
         ─► keyword ranking ─► reranker model keeps only on-topic passages
         ─► language model writes a cited answer ─► streamed to the browser
```

- **Libraries:** [Kiwix](https://www.kiwix.org) ZIM files, served by `kiwix-serve`.
- **Models:** run by [llamafile](https://github.com/mozilla-ai/llamafile), a single executable that works on every platform:

  | Model | Used for | Memory |
  |---|---|---|
  | [Qwen3-30B-A3B-Instruct-2507](https://huggingface.co/unsloth/Qwen3-30B-A3B-Instruct-2507-GGUF) | Standard answers | 32 GB+ |
  | [Qwen3-30B-A3B-Thinking-2507](https://huggingface.co/unsloth/Qwen3-30B-A3B-Thinking-2507-GGUF) | Thorough answers (reasons first) | 32 GB+ |
  | [Qwen3-14B](https://huggingface.co/unsloth/Qwen3-14B-GGUF) | Medium tier, thinking switched off | 16 GB+ |
  | [Qwen3-4B-Instruct-2507](https://huggingface.co/unsloth/Qwen3-4B-Instruct-2507-GGUF) | Small machines; also picks search terms beside the thinking model | any |
  | [bge-reranker-v2-m3](https://huggingface.co/gpustack/bge-reranker-v2-m3-GGUF) | Judging which passages answer the question | ~1 GB |

- **App:** one Go program (no dependencies beyond the standard library) with an embedded single-page UI.

## Default libraries

Wikipedia (with images), WikiMed, Wikivoyage, iFixit, MedlinePlus, NHS medicines, CDC travel health, military field medicine, US Army field manuals, Ready.gov, Appropedia, CD3WD, Energypedia, Low-tech Magazine, Gardenology, public-domain recipes, Wikibooks, Wiktionary, LibreTexts (12 subjects) and Stack Exchange Q&A sites for DIY, electronics, car mechanics, woodworking, gardening, cooking, homebrewing, outdoors, bicycles, aviation, amateur radio and sustainability.

That's about 200 GB in all; a 512 GB drive leaves room for updates. Project Gutenberg is listed but switched off (206 GB). Libraries are switched on or off in the drive's `config.json`.

Two extra libraries can be made locally with Kiwix's [zimit](https://github.com/openzim/zimit) crawler (see below): the NHS Health A–Z and the UK Highway Code.

## Building and preparing a drive

Needs Go 1.22+.

```sh
go test ./...
./build.sh /Volumes/MYDRIVE    # builds for macOS (arm64/amd64), Linux (amd64/arm64), Windows (amd64)
```

Format the drive as **exFAT** so every system can read it. Then, on the drive:

- `update.command` (Mac) or `update.bat` (Windows) downloads everything (about 200 GB; leave it overnight).
- `start.command`, `start.bat` or `sh start.sh` starts Offline Wiki and opens it in the browser.
- `install-schedule.command` (Mac, optional) updates the drive automatically.

See [README.txt](README.txt), which goes on the drive, for day-to-day use.

### Making the NHS and Highway Code libraries

These sites aren't published as Kiwix files. Both are © Crown copyright, reusable under the [Open Government Licence v3.0](https://www.nationalarchives.gov.uk/doc/open-government-licence/version/3/). Crawl them into a Docker volume, then copy the result into the drive's `library/` folder:

```sh
docker volume create ow-build
docker run --rm -v ow-build:/output ghcr.io/openzim/zimit zimit \
  --seeds https://www.nhs.uk/conditions/,https://www.nhs.uk/tests-and-treatments/,https://www.nhs.uk/mental-health/ \
  --scopeType custom --scopeIncludeRx '^https://www\.nhs\.uk/(conditions|tests-and-treatments|mental-health)/' \
  --scopeExcludeRx '(\?|/your-birth-plan\.aspx)' \
  --name nhs.uk_en_health-a-z --title "NHS Health A to Z" --description "NHS conditions, tests, treatments and mental health guidance" \
  --workers 4 --behaviors autofetch --waitUntil load
# Highway Code: --seeds https://www.gov.uk/guidance/the-highway-code
#   --scopeIncludeRx '^https://www\.gov\.uk/guidance/the-highway-code' --name gov.uk_en_highway-code
```

Write to a Docker volume rather than straight to the USB drive: on macOS, Docker Desktop reports the system disk's free space for anything under `/Volumes`, and the final ZIM file didn't appear when it was written straight to an exFAT drive.

## Layout

| Path | What |
|---|---|
| `main.go` | Command-line entry: `run`, `update`, `init`, `status`, `install-schedule` |
| `run.go`, `server.go` | Starting kiwix-serve and the models; HTTP API and UI server |
| `rag.go`, `extract.go`, `llm.go` | Search, article extraction, passage ranking and reranking, prompting |
| `update.go`, `updater.go`, `download.go` | Updater (resumable, checksum-verified) and the in-page Update button |
| `remount_darwin.go` | Read-only and read-write switching on macOS |
| `ui/index.html` | The chat page |
| `drive/` | Launcher scripts copied to the drive |
| `tools/` | Scripts for timing and testing questions against a running instance |

## Limits

Answers come from a local model reading what the search finds. It is told to answer only from those passages and to cite them, but it can still miss or misread things. For anything that matters, open the cited article and check.

The library content is not in this repository. Each library carries its own licence (mostly CC BY-SA, public domain or the Open Government Licence).

## Licence

The code is released under the [MIT Licence](LICENSE). The library content and models it downloads keep their own licences.
