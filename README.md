# zoom-taker

Record Zoom meetings locally as two audio tracks, transcribe them with Whisper,
and merge them into a speaker-labeled markdown transcript — all on your own machine.

> ⚠️ Only record meetings you are allowed to record. Respect your organization's
> policies and local law; this tool does no privileged capture — it records the
> audio your browser is already allowed to access.

## How it works

```
Zoom meeting (browser client)
   │
   ├─ tab audio ──► REMOTE track ─┐  record-zoom.html (MediaRecorder, webm/opus)
   └─ microphone ► ME track      ─┘  → uploaded to server.py → recordings/
                                        │
                                        ▼
                          transcribe_meeting.py (per track)
                          · Whisper transcription (MLX on macOS / faster-whisper in Docker)
                          · hallucination cleaning: silence overlap, repeat loops,
                            timestamp drift, quiet segments (RMS energy)
                          · speaker bleed merge: mic text matching the tab track in
                            time+text is attributed to REMOTE
                                        │
                                        ▼
                        output/<name>-transcript.md   [mm:ss] REMOTE/ME: text
```

Two-track recording is what keeps speaker separation: mixing to a single track
loses speakers and makes hallucinations much worse.

The tab-audio capture path needs **no macOS screen-recording (TCC) permission** —
it uses Chrome's tab-share audio. Requirements: Chrome microphone permission and a
Zoom meeting that allows joining from the browser client.

## Get started (prebuilt Docker image)

Multi-arch images (`linux/amd64` + `linux/arm64`) are built by GitHub Actions and
published to Docker Hub — no local build needed:

```bash
docker pull guri/zoom-taker:latest

mkdir -p model-cache recordings output
docker run --rm -p 8899:8899 \
  -v "$PWD/model-cache:/app/model-cache" \
  -v "$PWD/recordings:/app/recordings" \
  -v "$PWD/output:/app/output" \
  guri/zoom-taker:latest
```

Open <http://localhost:8899> and record — recordings and transcripts land in the
mounted folders, and the Whisper model downloads into `model-cache/` on first use
(the container runs offline afterwards). CLI-only transcription with the same
image:

```bash
docker run --rm \
  -v "$PWD/model-cache:/app/model-cache" \
  -v "$PWD/output:/app/output" \
  guri/zoom-taker:latest \
  python transcriber.py sample_test.aiff
```

Prefer compose? Replace `build: .` with `image: guri/zoom-taker:latest` in
`docker-compose.yml` and run `docker compose up`.

## Quick start (macOS, Apple Silicon)

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python downloader.py        # downloads the model into ./model-cache (once)

.venv/bin/python server.py            # http://localhost:8899
```

Open <http://localhost:8899>, run the self-check, join the Zoom meeting in a
browser tab, then: **Start recording** → pick the Zoom tab → check **Share audio**
→ **Stop & save** (recordings upload automatically) → **Transcribe**.

## Quick start (Docker, build from source)

```bash
docker compose up --build             # http://localhost:8899
```

`model-cache/`, `recordings/`, and `output/` are volume-mounted, so recordings,
transcripts, and the downloaded model all persist on the host. Inside the Linux
container the STT backend is faster-whisper (CPU); the first transcription
downloads the model into `./model-cache/` — afterwards the container runs offline.

Transcribe without the browser:

```bash
docker compose run --rm zoom-taker python transcriber.py sample_test.aiff
```

## CLI

```bash
# full meeting pipeline (both tracks, cleaning, merge):
python transcribe_meeting.py <meeting-name> [--language kr|en|cn]

# single audio file:
python transcriber.py <file> [--language kr|en|cn] [--backend mlx|faster]
./run.sh sample_test.aiff              # smoke test with the bundled sample
```

## Languages

`kr` (Korean), `en` (English), `cn` (Chinese) are accepted everywhere — the web
UI selector, `--language`, and the `TRANSCRIBE_LANGUAGE` environment variable.
They map to Whisper language codes internally:

| value | Whisper code |
|-------|--------------|
| `kr`  | `ko`         |
| `en`  | `en`         |
| `cn`  | `zh`         |

Two-language combos like **`kr+en`** are also accepted: the file is transcribed
once per language (2x runtime), the primary pass keeps its time windows, and the
secondary pass fills only the regions the primary missed — this recovers pure
foreign sentences that a single-language pass drops (see limitations below).
Use the meeting's dominant language first, e.g. `TRANSCRIBE_LANGUAGE=kr+en`.

To add another language, extend `LANGUAGE_CODES` in `transcriber.py`.

## Configuration

All configuration is via environment variables (see `docker-compose.yml` for the
Docker names):

| variable | default | meaning |
|----------|---------|---------|
| `WHISPER_BACKEND` | auto | `mlx` (macOS/Apple Silicon) or `faster` (CPU); auto-detects the installed package |
| `WHISPER_MODEL` | per backend | HF repo id (`mlx-community/whisper-large-v3-turbo` / `mobiuslabsgmbh/faster-whisper-large-v3-turbo`) |
| `MODEL_CACHE_DIR` | `./model-cache` | local model cache (safe to copy between machines) |
| `RECORDINGS_DIR` | `./recordings` | where uploads are stored |
| `OUTPUT_DIR` | `./output` | where transcripts are written |
| `TRANSCRIBE_LANGUAGE` | `kr` | `kr`/`en`/`cn` |
| `HF_ENDPOINT` | — | HuggingFace-compatible endpoint (mirror/proxy) |
| `HF_TOKEN` | — | HuggingFace token (gated repos) |
| `HOST` / `PORT` | `127.0.0.1` / `8899` | server bind address (Docker sets `HOST=0.0.0.0`) |
| `MAX_UPLOAD_MB` | `2048` | upload size cap |
| `WHISPER_COMPUTE_TYPE` | `int8` | faster-whisper compute type |

## Corporate networks / offline use

If `huggingface.co` is unreachable (common on corporate intranets), point
`HF_ENDPOINT` at any HuggingFace-compatible mirror or proxy before the first
download:

```bash
export HF_ENDPOINT='https://your-hf-mirror.example.com'
python downloader.py
```

The model lives entirely in `MODEL_CACHE_DIR` — copy that directory to the
target machine (or mount it as a Docker volume) and every later run works
offline without touching the mirror.

## Known limitations

- Zoom must allow joining from the **browser client**; if the host disables the
  web client, the tab-audio path cannot attach to the meeting.
- **Muting the shared tab mutes the recording** (silent webm). Moving to other
  tabs is fine.
- Your own voice can echo into the tab track via Zoom's server echo; the bleed
  filter catches most of it — headphones are the reliable mitigation.
- One transcription runs at a time; long meetings on the CPU backend take a while.
- `server.py` is built for localhost use and accepts uploads from anyone who can
  reach it — do not expose it to untrusted networks.
- The hallucination filters were tuned on Korean meetings; other languages may
  need threshold tweaks (constants at the top of `transcribe_meeting.py`).
- **Mixed-language meetings**: Whisper's `language` parameter biases the whole
  file toward one language — measured on a kr/en/zh mixed sample, forcing `kr`
  drops pure English sentences and transliterates in-sentence English words
  ("launch date" → "론치 데이트"), while forcing `en` translates foreign speech
  into English instead of transcribing it. Set `TRANSCRIBE_LANGUAGE` to the
  meeting's dominant language; short foreign phrases survive, long foreign
  passages may be lost. Auto-detection (`language=None`) only samples the first
  30s and does not track mid-file language switches. For kr/en-mixed meetings
  use the **`kr+en`** combo mode, which runs both passes and stitches them —
  it recovers the dropped sentences at twice the transcription time.

## License

[MIT](LICENSE)
