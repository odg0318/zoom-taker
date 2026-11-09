"""Central configuration: environment variables with defaults + backend detection.

All directories resolve relative to this file's directory unless overridden via
environment variables, so the same tree works natively on macOS and inside Docker
(where compose mounts the directories to /app/*).

Environment variables:
  WHISPER_BACKEND      "mlx" | "faster"   (default: auto-detect installed package)
  WHISPER_MODEL        HF repo id         (default: per-backend, see DEFAULT_MODELS)
  MODEL_CACHE_DIR      default ./model-cache
  RECORDINGS_DIR       default ./recordings
  OUTPUT_DIR           default ./output
  TRANSCRIBE_LANGUAGE  kr | en | cn       (default: kr)
  HOST / PORT          default 127.0.0.1:8899
  MAX_UPLOAD_MB        default 2048
  WHISPER_COMPUTE_TYPE faster-whisper compute type (default: int8)
  HF_ENDPOINT          HuggingFace endpoint (set to a corporate mirror/proxy if
                       huggingface.co is unreachable; standard huggingface_hub
                       behavior, honored on first download only — the local
                       cache makes later runs offline)
  HF_TOKEN             HuggingFace token (gated repos)
"""
import importlib.util
import os
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent


def _env_path(key: str, default: pathlib.Path) -> pathlib.Path:
    value = os.environ.get(key)
    return pathlib.Path(value).expanduser() if value else default


MODEL_CACHE_DIR = _env_path("MODEL_CACHE_DIR", ROOT / "model-cache")
RECORDINGS_DIR = _env_path("RECORDINGS_DIR", ROOT / "recordings")
OUTPUT_DIR = _env_path("OUTPUT_DIR", ROOT / "output")

# Explicit backend selection; None = auto-detect in resolve_backend().
WHISPER_BACKEND = os.environ.get("WHISPER_BACKEND") or None

DEFAULT_MODELS = {
    "mlx": "mlx-community/whisper-large-v3-turbo",
    "faster": "mobiuslabsgmbh/faster-whisper-large-v3-turbo",
}
WHISPER_MODEL = os.environ.get("WHISPER_MODEL") or None

TRANSCRIBE_LANGUAGE = os.environ.get("TRANSCRIBE_LANGUAGE", "kr")

HOST = os.environ.get("HOST", "127.0.0.1")
PORT = int(os.environ.get("PORT", "8899"))
MAX_UPLOAD_MB = int(os.environ.get("MAX_UPLOAD_MB", "2048"))
WHISPER_COMPUTE_TYPE = os.environ.get("WHISPER_COMPUTE_TYPE", "int8")


def resolve_backend() -> str:
    """Return the STT backend to use: "mlx" or "faster".

    WHISPER_BACKEND wins when set; otherwise detect whichever package is
    installed using find_spec only (no heavy imports, no downloads), so it is
    safe to call at server startup and in the health endpoint.
    """
    if WHISPER_BACKEND:
        if WHISPER_BACKEND not in DEFAULT_MODELS:
            raise SystemExit(f"Unknown WHISPER_BACKEND '{WHISPER_BACKEND}' (expected: mlx or faster)")
        return WHISPER_BACKEND
    if importlib.util.find_spec("mlx_whisper"):
        return "mlx"
    if importlib.util.find_spec("faster_whisper"):
        return "faster"
    raise SystemExit(
        "No STT backend installed.\n"
        "  macOS (Apple Silicon):  pip install -r requirements.txt\n"
        "  Linux/Docker (CPU):     pip install -r requirements-docker.txt"
    )


def model_repo_id(backend: str) -> str:
    """HF repo id for the backend; WHISPER_MODEL overrides both defaults."""
    return WHISPER_MODEL or DEFAULT_MODELS[backend]
