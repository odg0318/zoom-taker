#!/bin/bash
# Single-file transcription smoke test: ./run.sh sample_test.aiff
# Prefers the local .venv interpreter; override with PYTHON=/path/to/python.
set -euo pipefail

if [ -z "${PYTHON:-}" ] && [ -x ".venv/bin/python" ]; then
  PYTHON=".venv/bin/python"
fi
PYTHON="${PYTHON:-python3}"

INPUT="${1:-sample_test.aiff}"

exec "$PYTHON" transcriber.py "$INPUT" --language "${TRANSCRIBE_LANGUAGE:-kr}"
