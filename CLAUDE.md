# zoom-taker — Zoom meeting recording → transcription pipeline

Local tool: records Zoom meetings as two browser audio tracks, transcribes them
with whisper.cpp (Metal on macOS / CPU in Docker), cleans hallucinations, and
merges into a speaker-labeled markdown transcript. Shipped as a single Go
binary (rewritten from the original Python/MLX stack, 2026-11-10); README.md
is the user-facing doc and configuration reference.

## Progress

- [x] Go rewrite of the whole stack (config, model cache, STT, audio wrappers,
      filters, server, CLI) — Python implementation removed in the same branch
- [x] whisper.cpp v1.9.5 engine via official Go binding (submodule + replace
      directive); Metal verified from outside the repo (embedded shaders)
- [x] Wire-compatible server (record-zoom.html untouched, embedded)
- [x] Behavior parity tests: match (difflib), pipeline filters, server wire table
- [x] kr fixture ground truth + kr+en mixed-language check (large-v3-turbo)
- [x] Server E2E on the Go binary (health/page/uploads/transcribe/status/
      transcript/traversal) + SIGINT mid-transcription → exit 0 in <100ms,
      no orphan ffmpeg
- [x] Engine-delta eyeball vs the Python-era transcript of the real meeting
      2026-11-09-22-05 pair: content/order/timeline match; speaker labels shift
      on overlapping lines (engine segment boundaries move the ±3s/0.6 bleed
      matches) — accepted, content-based (no golden files)
- [x] Docker multi-stage CGO build (native build verified; compose E2E skipped
      this round per user — macOS runs the native Metal binary, CI builds the
      images on push)
- [ ] Real-world 10min+ meeting endurance test (dropouts/silence/track drift —
      longest verified session is 112s)
- [ ] Echo dedup improvement: mic segment that is a partial substring of a system
      sentence should be judged bleed (current similarity 0.6 misses that case)
- [ ] arm64 QEMU Docker build validation via workflow dispatch (native arm64
      build passed locally; QEMU path untested)

## Directory structure

```
go.mod / go.sum         # module github.com/odg0318/zoom-taker, go 1.23;
                        # replace directive pins the binding to the submodule
embed.go                # //go:embed record-zoom.html (package app)
Makefile                # build / whisper-lib / test / test-pure / vet / docker / clean
cmd/zoom-taker/main.go  # serve (default) | meeting | transcribe | download
internal/
  config/               # env-var configuration (single source of config)
  modelcache/           # offline-first GGML resolve + HF download (resume, sha256)
  stt/                  # Engine interface + whisper.cpp cgo engine (build tag "whisper")
  audio/                # ffmpeg wrappers: 16kHz decode, duration (webm fallback), silencedetect, RMS
  match/                # normalize + difflib ratio (rune-fed, Python-parity tested)
  pipeline/             # language passes, filters, bleed merge, markdown + testdata wavs
  server/               # embedded page, uploads, job manager, wire-parity tests
third_party/whisper.cpp # git submodule @ v1.9.5 (binding + C sources)
record-zoom.html        # browser 2-track recorder (wire-compat client, embedded)
sample_test.aiff        # synthetic TTS sample for smoke tests (tracked on purpose)
Dockerfile / docker-compose.yml / .dockerignore
.github/workflows/docker-publish.yml  # multi-arch image → Docker Hub (guri/zoom-taker)
model-cache/            # GGML model cache (gitignored — copy between machines)
recordings/ output/     # gitignored meeting data
build/ bin/             # whisper.cpp build staging + binary (gitignored)
```

## Environment (personal laptop, 2026-11-10)

- Apple M2 / 8GB RAM / macOS 27.2
- go 1.27.3 (Homebrew), cmake, ffmpeg (ffmpeg-full), Docker Desktop
- whisper.cpp v1.9.5 built static; Metal shaders embedded (GGML_METAL_EMBED_LIBRARY
  default ON — never override Apple cmake defaults)
- Model: ggml-large-v3-turbo-q5_0.bin (574MB) in model-cache/ — HF repo
  ggerganov/whisper.cpp, direct resolve download (302→CDN, Range resume)

## Build & run (verified)

```bash
make build              # whisper.cpp libs → build/stage → bin/zoom-taker (cgo, -tags whisper)
make test               # full suite incl. cgo engine tests
make test-pure          # go build/test without C toolchain (all cgo behind the "whisper" tag)
./bin/zoom-taker serve  # http://localhost:8899  (Docker: docker compose up --build)
./bin/zoom-taker meeting <name> [--language kr|en|cn|kr+en]
./bin/zoom-taker transcribe <file> [--language ...]
./bin/zoom-taker download [model]
```

Browser flow is unchanged from the Python era: self-check → join Zoom Web →
● Start → share picker (Zoom TAB + "Share audio") → ■ Stop → Transcribe.

## Transcription cleaning pipeline (internal/pipeline/clean.go)

Same order and thresholds as the Python original (all parity-tested):

1. Drop timestamps beyond audio duration (track drift)
2. Drop segments ≥50% overlapping silence (ffmpeg silencedetect −45dB/1.5s)
3. Drop consecutive identical-text repeat loops (empty normalized text never repeats)
4. **Energy filter**: drop segments −15dB below the track's speech peak or below −55dB
   (bleed escapes silence detection but trips the energy filter — measured: −56dB vs peak −38dB)
5. Mic segment overlapping a system segment in time (±3s) + text (difflib ratio > 0.6)
   is judged bleed and attributed to REMOTE (stable sort: REMOTE first on ties)

Language handling: kr→ko/en→en/cn→zh; combos run one pass per language per track
(decoded once), secondary fills regions with <0.5 primary coverage. Markdown:
`# <name> transcript` + `[MM:SS] SPEAKER: text`. RMS replicates the audioop math
exactly (integer floor(sqrt(mean(v²))), 20·log10(rms/32768)).

## Lessons / known issues

- **Transcribing a mixed-down m4a loses speaker separation and hallucinates badly**
  → always transcribe per track
- My own speech returns via **Zoom server echo** into the system (tab) track and
  gets labeled REMOTE — the bleed filter only catches similarity > 0.6, so mic
  segments split into partial sentences slip through
  → mitigation: headphones, or the substring-matching TODO
- Mic peaked at −0.1dB (right before clipping) — speak at lower input volume
- **Muting the shared tab = silent recording** (moving to other tabs is fine)
- Zoom Web cannot join at all if the host disables the web client — check per meeting
- Model snapshot hashes differ per environment → (Python-era note) the Go model
  cache stores plain files + a .json meta (size/sha256); offline-first on size match
- Filter constants live at the top of internal/pipeline/clean.go:
  SILENCE_NOISE_DB=−45, SILENCE_MIN_DUR=1.5, SILENCE_OVERLAP_MAX=0.5,
  QUIET_DROP_DB=15, QUIET_FLOOR_DB=−55, TIME_WINDOW_S=3.0, SIMILARITY_THRESHOLD=0.6,
  LANG_MERGE_OVERLAP=0.5
- Measured data: track drift 0.06s across two real meetings (negligible); bleed
  segments sit at −56dB vs −38dB speech peak; longest verified session 112s
- **Engine delta (mlx → whisper.cpp large-v3-turbo q5_0)**: the old "forcing kr
  transliterates in-sentence English / drops pure English sentences" behavior did
  NOT reproduce on whisper.cpp — kr-forced transcription of a kr/en TTS mix
  recovered the English sentence verbatim, and kr+en dual-pass produced the same
  output. Keep `kr+en` for safety on long real meetings; revisit if a real meeting
  shows dropped foreign passages.
- **kr fixture ground truth** (sample_test.aiff, large-v3-turbo q5_0, Metal,
  language kr — exact expected output):
  `[00:00] 안녕하세요. 이것은 한국어 전사 테스트용 샘플 오디오입니다.` /
  `[00:06] 회의 녹음 파이프라인이 정상적으로 동작하는지 확인합니다.`
- whisper.cpp cancellation aborts at the ~30s encoder window boundary (the Python
  subprocess died instantly) — Ctrl-C latency is up to ~30s, documented in README
- wire diffs accepted in the Go port (invisible to the record-zoom.html client):
  compact JSON spacing, non-GET/POST → 404 (Python BaseHTTP said 501), percent-
  decoded URL paths, non-object JSON body → 400 invalid JSON (Python: 500), and
  the /api/output regex is ASCII-only (Python \w matched Unicode names that can
  never be created since names are already ASCII-sanitized)
- ffmpeg sine source defaults to 1/8 amplitude (−18dB peak): a test "loud" tone
  needs explicit volume; silence-range fixtures must exceed 1.5s to register
- Test fixture trick (internal/pipeline/testdata/quiet.wav): sparse clicks —
  peak stays above the −45dB silencedetect threshold while 1% duty cycle puts RMS
  at −63dB below the floor, so the quiet segment reaches the energy filter the
  way real mic bleed does

## Browser E2E automation notes (2026-11-09, Python era)

Verified via a scripted Chrome (`--remote-debugging-port=9222`) driven over raw
CDP (`/json` → `webSocketDebuggerUrl`); chrome-devtools MCP is installed for
interactive use, but MCP servers added mid-session do not reach subagents, so
automation used raw CDP.

**Verified green** (Python server): full page flow 6/6 (server-mode health banner
→ capture starts unattended → record → stop → both tracks auto-upload →
Transcribe button → job log streaming → done + transcript link; console clean),
language selector (kr preselected, kr+en/en/cn present), upload→transcribe→
content via sample wav, Docker + faster-whisper, kr+en dual-pass merge (English
sentence recovered at [00:03]).

**Not reproducible in automation Chrome** (3 triangulated runs): the captured
tab's *digital* audio never reaches `getDisplayMedia` on this macOS —
`--use-fake-device-for-media-stream` poisons tab capture with synthetic beeps,
generic `--auto-select-desktop-capture-source` resolves to screen/window
(acoustic room audio without TCC), and tab-specific
`--auto-select-tab-capture-source-by-title` yields a near-silent sink
(peak −27dBFS, waveform correlation 0.02 vs source). The recorder page itself
is exonerated: real-hardware tab capture already measured −8.1dB over 74s of
Zoom Web, and the same automation captured room audio fine. Manual spot-check
on real Chrome remains the only way to verify capture content. Useful anyway:
`video: { displaySurface: "browser" }` (committed) makes the share picker open
on the "tab" category, and `--use-fake-ui-for-media-stream` +
`--autoplay-policy=no-user-gesture-required` work fine for the non-audio flow.
