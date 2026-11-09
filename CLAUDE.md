# zoom-taker — Zoom meeting recording → transcription pipeline

Local tool: records Zoom meetings as two browser audio tracks, transcribes them
with Whisper (MLX on macOS / faster-whisper in Docker), cleans hallucinations,
and merges into a speaker-labeled markdown transcript. Setup and verified on a
personal MacBook (2026-11-09); see README.md for user-facing docs and configuration.

## Progress

- [x] STT model + transcription pipeline (hallucination cleaning included)
- [x] Recording path A: audiorec (macOS app target capture) — verified in 2 real meetings
- [x] Recording path B: browser 2-track recorder (record-zoom.html) — verified live on Zoom Web
- [x] Transcription cleaning/merging (silence/energy/repeat filters + ME/REMOTE bleed detection)
- [x] Web server (server.py): serves the recorder page, uploads to recordings/,
      background transcription with log polling, transcript serving
- [x] Dual backend: transcriber.py (mlx | faster-whisper), model caching via
      downloader.py + config.py (HF_ENDPOINT mirror support, offline after first download)
- [x] Docker: Dockerfile + docker-compose.yml (model-cache/recordings/output volumes)
- [ ] Real-world 10min+ meeting endurance test (dropouts/silence/track drift — longest
      verified session is 112s)
- [ ] Echo dedup improvement: mic segment that is a partial substring of a system
      sentence should be judged bleed (current similarity 0.6 misses that case)

## Directory structure

```
config.py             # env-var configuration + backend detection (single source of config)
transcriber.py        # backend abstraction (mlx | faster-whisper), model cache resolution, single-file CLI
transcribe_meeting.py # per-track transcription → cleaning → ME/REMOTE merge → output/<name>-transcript.md
downloader.py         # model download into MODEL_CACHE_DIR (honors HF_ENDPOINT/HF_TOKEN)
server.py             # stdlib-only web server: page serving, uploads, background transcription jobs
record-zoom.html      # browser 2-track recorder (self-check built in, server upload + transcribe button)
run.sh                # single-file smoke test (prefers .venv interpreter)
requirements.txt      # macOS native: mlx-whisper, huggingface_hub
requirements-docker.txt # Docker/Linux: faster-whisper, huggingface_hub
Dockerfile / docker-compose.yml / .dockerignore
sample_test.aiff      # synthetic TTS sample for smoke tests (tracked on purpose)
model-cache/          # HuggingFace model cache (gitignored — copy between machines)
recordings/           # recordings (gitignored)
output/               # transcripts + per-track JSON (gitignored)
.venv/                # Python virtualenv (gitignored)
```

## Environment (personal laptop, 2026-11-09)

- Apple M2 / 8GB RAM / macOS 27.2 / CLT 27.2
- `.venv` (Python 3.11 pyenv) + mlx-whisper + huggingface_hub
- ffmpeg (Homebrew, ffmpeg-full) — required by whisper audio decoding and the
  silence/energy filters
- audiorec 1.1.0 (path A, MIT) with Screen Recording + Microphone TCC granted

## Workflows (verified)

### Path B: browser recorder (primary, no TCC needed)

```bash
.venv/bin/python server.py    # http://localhost:8899  (Docker: docker compose up --build)
# 1. Self-check (mic permission, display-capture, secure context) + live level tests
# 2. Join the Zoom meeting in a browser tab ("Join from your browser")
# 3. ● Start → share picker: pick the Zoom TAB + check "Share audio" (never whole screen)
# 4. ■ Stop → auto-uploads to recordings/, then Transcribe on the page
.venv/bin/python transcribe_meeting.py <name>   # or the Transcribe button
```

### Path A: audiorec (Zoom desktop app, needs Screen Recording TCC)

```bash
audiorec record --target-bundle-id us.zoom.xos \
  --output ~/path/to/zoom-taker/recordings --name <name>
# Ctrl-C to stop (SIGINT/SIGTERM finalizes; -system.caf/-mic.caf + tracks.json)
.venv/bin/python transcribe_meeting.py <name>
```

### Single-file smoke test

```bash
./run.sh sample_test.aiff   # backend auto-detected, --language from TRANSCRIBE_LANGUAGE
```

## Transcription cleaning pipeline (transcribe_meeting.py)

1. Drop timestamps beyond audio duration (track drift)
2. Drop segments ≥50% overlapping silence (ffmpeg silencedetect -45dB/1.5s)
3. Drop consecutive identical-text repeat loops
4. **Energy filter**: drop segments −15dB below the track's speech peak or below −55dB
   (bleed escapes silence detection but trips the energy filter — measured: −56dB vs peak −38dB)
5. Mic segment overlapping a system segment in time (±3s) + text (similarity 0.6)
   is judged bleed and attributed to REMOTE
6. webm duration N/A handling: Chrome MediaRecorder omits duration in the header →
   measured by full decode

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
- Model snapshot hashes differ per environment → model resolution globs dynamically,
  nothing to maintain
- Filter parameters: SILENCE_NOISE_DB=-45, SILENCE_MIN_DUR=1.5, SILENCE_OVERLAP_MAX=0.5,
  QUIET_DROP_DB=15, QUIET_FLOOR_DB=-55, TIME_WINDOW_S=3.0, SIMILARITY_THRESHOLD=0.6
  (top of transcribe_meeting.py)
- Measured data: track drift 0.06s across two real meetings (negligible); bleed
  segments sit at −56dB vs −38dB speech peak; longest verified session 112s
- Python ≥3.13 removed `audioop` → `pcm_rms_db` falls back to an array-based RMS
  with identical math; Docker image pins python:3.11-slim
- Mixed-language meetings (measured, TTS kr/en/zh sample): forcing one language
  biases the whole file — kr drops pure English sentences and transliterates
  in-sentence English ("launch date"→"론치 데이트"); en TRANSLATES foreign speech
  instead of transcribing; language=None auto-detects once from the first 30s and
  does not follow mid-file switches. Chinese text survives ko-forcing; use the
  meeting's dominant language
- MLX does not run inside Linux containers → Docker uses faster-whisper (CPU,
  int8); backend selection via WHISPER_BACKEND or auto-detection (config.py)
