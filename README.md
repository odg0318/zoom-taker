# zoom-taker

Record Zoom meetings locally as two audio tracks, transcribe them with
[whisper.cpp](https://github.com/ggml-org/whisper.cpp), and merge them into a
speaker-labeled markdown transcript — all on your own machine, from a single
self-contained binary.

> ⚠️ Only record meetings you are allowed to record. Respect your organization's
> policies and local law; this tool does no privileged capture — it records the
> audio your browser is already allowed to access.

## How it works

```
Zoom meeting (browser client)
   │
   ├─ tab audio ──► REMOTE track ─┐  record-zoom.html (MediaRecorder, webm/opus)
   └─ microphone ► ME track      ─┘  → uploaded to the server → recordings/
                                        │
                                        ▼
                          zoom-taker (per track, in-process)
                          · whisper.cpp transcription (Metal on macOS / CPU in Docker)
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
mounted folders, and the Whisper model (GGML `large-v3-turbo` q5_0, 574MB)
downloads into `model-cache/` on first use (the container runs offline
afterwards). CLI-only transcription with the same image:

```bash
docker run --rm \
  -v "$PWD/model-cache:/app/model-cache" \
  -v "$PWD/output:/app/output" \
  guri/zoom-taker:latest \
  zoom-taker transcribe sample_test.aiff
```

Prefer compose? Replace `build: .` with `image: guri/zoom-taker:latest` in
`docker-compose.yml` and run `docker compose up`.

## Quick start (macOS, Apple Silicon)

Native builds use whisper.cpp with Metal (shaders are embedded in the binary —
no runtime model files needed):

```bash
brew install go cmake ffmpeg
make                    # whisper.cpp static libs + bin/zoom-taker

./bin/zoom-taker download   # downloads the model into ./model-cache (once)
./bin/zoom-taker serve      # http://localhost:8899
```

Open <http://localhost:8899>, run the self-check, join the Zoom meeting in a
browser tab, then: **Start recording** → pick the Zoom tab → check **Share audio**
→ **Stop & save** (recordings upload automatically) → **Transcribe**.

## Quick start (Docker, build from source)

```bash
git clone --recurse-submodules https://github.com/odg0318/zoom-taker
cd zoom-taker
docker compose up --build               # http://localhost:8899
```

`model-cache/`, `recordings/`, and `output/` are volume-mounted, so recordings,
transcripts, and the downloaded model all persist on the host. Inside the Linux
container whisper.cpp runs on CPU; the first transcription downloads the model
into `./model-cache/` — afterwards the container runs offline.

Transcribe without the browser:

```bash
docker compose run --rm zoom-taker zoom-taker transcribe sample_test.aiff
```

## CLI

```bash
zoom-taker                       # serve (default)
zoom-taker serve                 # web server + recorder page
zoom-taker meeting <name>        # full pipeline: recordings/<name>-{system|mic}.* → transcript
zoom-taker transcribe <file>     # single audio file, no speaker labels
zoom-taker download [model]      # fetch the GGML model into the cache

./bin/zoom-taker transcribe sample_test.aiff   # smoke test with the bundled sample
```

All commands accept `--language` (where it applies) and read the standard
configuration from the environment.

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

To add another language, extend `languageCodes` in `internal/pipeline/language.go`.

## Configuration

All configuration is via environment variables (see `docker-compose.yml` for the
Docker names):

| variable | default | meaning |
|----------|---------|---------|
| `WHISPER_MODEL` | `ggml-large-v3-turbo-q5_0.bin` | GGML file name in the HF `ggerganov/whisper.cpp` repo, or a path to a local `.bin` |
| `MODEL_CACHE_DIR` | `./model-cache` | local model cache (safe to copy between machines) |
| `RECORDINGS_DIR` | `./recordings` | where uploads are stored |
| `OUTPUT_DIR` | `./output` | where transcripts are written |
| `TRANSCRIBE_LANGUAGE` | `kr` | `kr`/`en`/`cn` or a combo like `kr+en` |
| `HF_ENDPOINT` | — | HuggingFace-compatible endpoint (mirror/proxy) |
| `HF_TOKEN` | — | HuggingFace token (private mirrors only; the model repo is public) |
| `HOST` / `PORT` | `127.0.0.1` / `8899` | server bind address (Docker sets `HOST=0.0.0.0`) |
| `MAX_UPLOAD_MB` | `2048` | upload size cap |

## Building from source

```bash
make build     # whisper.cpp static libs + bin/zoom-taker (Metal on macOS, CPU on Linux)
make test      # full test suite (needs whisper-lib)
make test-pure # tests without cgo (no whisper.cpp build needed)
make vet       # go vet for both pure and cgo builds
```

The whisper.cpp source is pinned as a git submodule (`third_party/whisper.cpp`);
the Go binding for it lives in the same tree and is linked via a `replace`
directive. Server pages, error strings, and filter behavior are covered by
wire/behavior parity tests.

## Corporate networks / offline use

If `huggingface.co` is unreachable (common on corporate intranets), point
`HF_ENDPOINT` at any HuggingFace-compatible mirror or proxy before the first
download:

```bash
export HF_ENDPOINT='https://your-hf-mirror.example.com'
./bin/zoom-taker download
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
- One transcription runs at a time; long meetings on CPU take a while. Cancel
  via Ctrl-C — decoding stops at the next ~30s encoder window boundary.
- The server is built for localhost use and accepts uploads from anyone who can
  reach it — do not expose it to untrusted networks.
- The hallucination filters were tuned on Korean meetings; other languages may
  need threshold tweaks (constants at the top of `internal/pipeline/clean.go`).
- **Mixed-language meetings**: Whisper's `language` parameter biases the whole
  file toward one language. Forcing the dominant language worked well in
  practice on the whisper.cpp `large-v3-turbo` engine — including in-sentence
  English inside Korean speech — but long foreign passages may still be lost.
  Auto-detection only samples the first ~30s and does not track mid-file
  language switches. For kr/en-mixed meetings use the **`kr+en`** combo mode,
  which runs both passes and stitches them (2x transcription time).

## License

[MIT](LICENSE) — whisper.cpp and its Go binding are MIT as well.
