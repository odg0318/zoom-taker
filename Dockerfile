FROM python:3.11-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements-docker.txt ./
RUN pip install --no-cache-dir -r requirements-docker.txt

COPY config.py transcriber.py downloader.py transcribe_meeting.py server.py record-zoom.html run.sh sample_test.aiff ./

ENV HOST=0.0.0.0 \
    PORT=8899 \
    MODEL_CACHE_DIR=/app/model-cache \
    RECORDINGS_DIR=/app/recordings \
    OUTPUT_DIR=/app/output

EXPOSE 8899
CMD ["python", "server.py"]
