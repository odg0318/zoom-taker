"""Download the Whisper model for the active backend into the local cache.

If huggingface.co is unreachable from your network (e.g. a corporate
intranet), point HF_ENDPOINT at any HuggingFace-compatible mirror/proxy —
this is standard huggingface_hub behavior:

    export HF_ENDPOINT='https://your-hf-mirror.example.com'

HF_TOKEN can be set for gated repos. The cache (MODEL_CACHE_DIR, default
./model-cache) persists across runs and Docker volume mounts, so the mirror
is only needed for the very first download; everything afterwards runs
offline against the cache.

Usage:
    python downloader.py            # uses WHISPER_BACKEND / auto-detect
"""
import config
import transcriber

path = transcriber.resolve_model(config.resolve_backend())
print(f"Model ready: {path}")
