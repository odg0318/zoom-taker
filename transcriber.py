"""Whisper backend abstraction: MLX (macOS / Apple Silicon) and faster-whisper (CPU / Docker).

Both backends resolve models from the shared local cache (MODEL_CACHE_DIR) —
see resolve_model() — and return the same segment shape consumed by the
meeting pipeline: [{"start": float, "end": float, "text": str}, ...].

CLI (single-file mode, what run.sh calls):
    python transcriber.py <audiofile> [--language kr|en|cn] [--backend mlx|faster]
    -> writes output/<stem>-transcript.md

Backends are imported lazily: importing this module never loads a model or
touches the network.
"""
import argparse
import os
import pathlib

import config

# UI/env-facing language codes -> Whisper ISO-639-1 codes.
LANGUAGE_CODES = {"kr": "ko", "en": "en", "cn": "zh"}


def normalize_language(lang: str) -> str:
    """Map kr/en/cn to Whisper language codes; reject anything else."""
    code = LANGUAGE_CODES.get((lang or "").strip().lower())
    if not code:
        raise SystemExit(f"Unsupported language '{lang}' (expected one of: {', '.join(LANGUAGE_CODES)})")
    return code


def repo_to_cache_name(repo_id: str) -> str:
    """"org/name" -> huggingface_hub cache layout "models--org--name"."""
    return "models--" + repo_id.replace("/", "--")


def _snapshot_dir(repo_id: str) -> pathlib.Path | None:
    """Newest snapshot directory for a repo in the local cache, or None."""
    base = config.MODEL_CACHE_DIR / repo_to_cache_name(repo_id) / "snapshots"
    if not base.is_dir():
        return None
    snapshots = sorted(p for p in base.iterdir() if p.is_dir())
    return snapshots[0] if snapshots else None


def resolve_model(backend: str, repo_id: str | None = None) -> pathlib.Path:
    """Locate the model snapshot in the local cache, downloading it if missing.

    Downloads go through huggingface_hub, so HF_ENDPOINT (corporate mirror or
    proxy) and HF_TOKEN are honored. Cached trees resolve with
    local_files_only=True first: nothing touches the network once the model
    is present, which is what makes offline/intranet runs work.
    """
    repo_id = repo_id or config.model_repo_id(backend)
    found = _snapshot_dir(repo_id)
    if found:
        return found

    from huggingface_hub import snapshot_download

    def download(local_files_only: bool) -> pathlib.Path:
        return pathlib.Path(snapshot_download(
            repo_id=repo_id,
            cache_dir=str(config.MODEL_CACHE_DIR),
            endpoint=os.environ.get("HF_ENDPOINT"),
            token=os.environ.get("HF_TOKEN"),
            local_files_only=local_files_only,
        ))

    try:                                    # cached in hub's blobs layout? still offline
        path = download(local_files_only=True)
    except Exception:
        path = download(local_files_only=False)
    # snapshot_download returns the snapshot dir itself; the glob re-check
    # covers cache layouts where it returns a parent instead.
    found = _snapshot_dir(repo_id) or (path if path.is_dir() else None)
    if not found:
        raise SystemExit(f"Model snapshot not found after download: {repo_id}")
    return found


def transcribe(path: pathlib.Path, backend: str | None = None,
               language: str | None = None) -> list[dict]:
    """Transcribe an audio file into [{start, end, text}, ...] via either backend."""
    backend = backend or config.resolve_backend()
    whisper_lang = normalize_language(language or config.TRANSCRIBE_LANGUAGE)
    model_dir = resolve_model(backend)

    if backend == "mlx":
        import mlx_whisper

        result = mlx_whisper.transcribe(str(path), path_or_hf_repo=str(model_dir), language=whisper_lang)
        return [
            {"start": float(seg["start"]), "end": float(seg["end"]), "text": seg["text"].strip()}
            for seg in result["segments"]
        ]

    from faster_whisper import WhisperModel

    model = WhisperModel(str(model_dir), device="cpu", compute_type=config.WHISPER_COMPUTE_TYPE)
    segments, _info = model.transcribe(str(path), language=whisper_lang)
    return [
        {"start": float(seg.start), "end": float(seg.end), "text": seg.text.strip()}
        for seg in segments
    ]


def fmt(seconds: float) -> str:
    return f"{int(seconds) // 60:02d}:{int(seconds) % 60:02d}"


def main():
    parser = argparse.ArgumentParser(description="Single-file Whisper transcription (backend auto-selected).")
    parser.add_argument("file", type=pathlib.Path, help="audio file to transcribe")
    parser.add_argument("--language", default=config.TRANSCRIBE_LANGUAGE,
                        help=f"meeting language: {', '.join(LANGUAGE_CODES)} (default: %(default)s)")
    parser.add_argument("--backend", choices=sorted(config.DEFAULT_MODELS), default=None,
                        help="override backend (default: auto-detect)")
    args = parser.parse_args()

    # Convenience: a bare filename that only exists in RECORDINGS_DIR (e.g. an
    # uploaded "<name>-remote.webm") resolves there automatically.
    path = args.file
    if not path.exists():
        in_recordings = config.RECORDINGS_DIR / path.name
        if in_recordings.exists():
            path = in_recordings
        else:
            raise SystemExit(f"File not found: {path}")

    segments = transcribe(path, backend=args.backend, language=args.language)
    config.OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    out_path = config.OUTPUT_DIR / f"{args.file.stem}-transcript.md"
    with open(out_path, "w") as f:
        for seg in segments:
            f.write(f"[{fmt(seg['start'])}] {seg['text']}\n")
    print(f"Transcript saved: {out_path} ({len(segments)} segments)")


if __name__ == "__main__":
    main()
