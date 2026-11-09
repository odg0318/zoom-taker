"""Local server for zoom-taker: serves the recorder page, accepts recording
uploads, and runs transcriptions in the background.

Standard library only. Binds to HOST:PORT (default 127.0.0.1:8899) — this
server accepts uploads from anyone who can reach it, so do not expose it to
untrusted networks.

Endpoints:
  GET  /                      -> recorder page (record-zoom.html)
  GET  /api/health            -> {"ok", "backend", "model"}
  POST /api/upload            -> query: name, track=remote|me, ext=webm|caf|m4a|wav;
                                 body: raw file bytes -> saved into RECORDINGS_DIR
  POST /api/transcribe        -> JSON {"name", "language"} -> 202 {"job"}; 409 when busy
  GET  /api/status?job=<id>   -> job state + last log lines + transcript path
  GET  /api/output/<file>     -> finished transcript markdown

Usage:
    python server.py
"""
import collections
import dataclasses
import json
import pathlib
import re
import signal
import subprocess
import sys
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import config
import transcriber

ROOT = pathlib.Path(__file__).resolve().parent

UPLOAD_EXTS = {"webm", "caf", "m4a", "wav"}
NAME_RE = re.compile(r"^[A-Za-z0-9._-]{1,80}$")
OUTPUT_FILE_RE = re.compile(r"^[\w.-]+-transcript\.md$")
MAX_UPLOAD_BYTES = config.MAX_UPLOAD_MB * 1024 * 1024
MAX_JOBS = 20                      # remember the most recent N jobs
CHUNK = 64 * 1024


@dataclasses.dataclass
class Job:
    id: str
    name: str
    state: str = "queued"          # queued | running | done | error
    log: collections.deque = dataclasses.field(default_factory=lambda: collections.deque(maxlen=500))
    returncode: int | None = None
    transcript: str | None = None


JOBS: dict[str, Job] = {}
JOBS_LOCK = threading.Lock()
ACTIVE_JOB: str | None = None                    # at most one transcription at a time
CURRENT_CHILD: subprocess.Popen | None = None    # running transcription process


def sanitize_name(raw: str) -> str | None:
    """Meeting names must be a safe single path component, no leading dots."""
    if not raw or not NAME_RE.match(raw) or raw.startswith("."):
        return None
    return raw


def safe_resolve(dir_path: pathlib.Path, filename: str) -> pathlib.Path | None:
    """dir_path/filename as an absolute path, or None if it escapes dir_path."""
    candidate = (dir_path / filename).resolve()
    if candidate.parent != dir_path.resolve():
        return None
    return candidate


def job_payload(job: Job) -> dict:
    return {
        "job": job.id,
        "name": job.name,
        "state": job.state,
        "log": list(job.log)[-50:],
        "returncode": job.returncode,
        "transcript": job.transcript,
    }


def start_job(name: str, language: str) -> tuple[int, dict]:
    """Queue a transcription; only one may run at a time (409 otherwise)."""
    global ACTIVE_JOB
    with JOBS_LOCK:
        if ACTIVE_JOB and JOBS.get(ACTIVE_JOB, Job("", "")).state in ("queued", "running"):
            return 409, {"error": "a transcription is already running", "job": ACTIVE_JOB}
        job = Job(id=f"{name}-{int(time.time())}", name=name)
        JOBS[job.id] = job
        for old in sorted(JOBS)[:-MAX_JOBS]:
            del JOBS[old]
        ACTIVE_JOB = job.id
    threading.Thread(target=run_job, args=(job, language), daemon=True).start()
    return 202, {"job": job.id, "state": job.state}


def run_job(job: Job, language: str) -> None:
    """Run transcribe_meeting.py as a subprocess, streaming its output into job.log."""
    global ACTIVE_JOB, CURRENT_CHILD
    cmd = [sys.executable, str(ROOT / "transcribe_meeting.py"), job.name, "--language", language]
    job.state = "running"
    proc = None
    try:
        proc = subprocess.Popen(
            cmd, cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, bufsize=1,
        )
        with JOBS_LOCK:
            CURRENT_CHILD = proc
        for line in proc.stdout:
            job.log.append(line.rstrip("\n"))
        proc.wait()
        job.returncode = proc.returncode
        transcript = config.OUTPUT_DIR / f"{job.name}-transcript.md"
        if proc.returncode == 0 and transcript.exists():
            job.state = "done"
            job.transcript = f"/api/output/{transcript.name}"
        else:
            job.state = "error"
            if proc.returncode != 0:
                job.log.append(f"transcription exited with code {proc.returncode}")
    except Exception as e:                     # surface any failure inside the job log
        job.state = "error"
        job.log.append(f"server error: {e}")
    finally:
        with JOBS_LOCK:
            if CURRENT_CHILD is proc:
                CURRENT_CHILD = None
            if ACTIVE_JOB == job.id:
                ACTIVE_JOB = None


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    timeout = 60

    def log_message(self, fmt, *args):
        print(f"{self.address_string()} {fmt % args}")

    def send_json(self, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def send_file(self, path: pathlib.Path, content_type: str) -> None:
        body = path.read_bytes()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def send_page(self) -> None:
        page = ROOT / "record-zoom.html"
        if not page.is_file():
            return self.send_json(500, {"error": "record-zoom.html missing"})
        self.send_file(page, "text/html; charset=utf-8")

    def do_GET(self):
        parsed = urllib.parse.urlsplit(self.path)
        path, query = parsed.path, urllib.parse.parse_qs(parsed.query)
        try:
            if path in ("/", "/record-zoom.html"):
                return self.send_page()
            if path == "/api/health":
                try:
                    backend = config.resolve_backend()
                    model = config.model_repo_id(backend)
                except SystemExit:
                    backend, model = None, None
                return self.send_json(200, {"ok": True, "backend": backend, "model": model})
            if path == "/api/status":
                return self.handle_status(query)
            m = re.match(r"^/api/output/([^/]+)$", path)
            if m:
                return self.handle_output_file(m.group(1))
            return self.send_json(404, {"error": "not found"})
        except BrokenPipeError:
            pass
        except Exception as e:                 # keep the server alive on handler bugs
            self.send_json(500, {"error": str(e)})

    def handle_status(self, query: dict) -> None:
        job_id = (query.get("job") or [None])[0]
        if job_id:
            job = JOBS.get(job_id)
            if not job:
                return self.send_json(404, {"error": "unknown job"})
            return self.send_json(200, job_payload(job))
        with JOBS_LOCK:
            jobs = sorted(JOBS.values(), key=lambda j: j.id, reverse=True)
        return self.send_json(200, {"jobs": [job_payload(j) for j in jobs]})

    def handle_output_file(self, filename: str) -> None:
        filename = urllib.parse.unquote(filename)
        if not OUTPUT_FILE_RE.match(filename):
            return self.send_json(400, {"error": "invalid file name"})
        target = safe_resolve(config.OUTPUT_DIR, filename)
        if not target or not target.is_file():
            return self.send_json(404, {"error": "not found"})
        return self.send_file(target, "text/markdown; charset=utf-8")

    def do_POST(self):
        parsed = urllib.parse.urlsplit(self.path)
        path, query = parsed.path, urllib.parse.parse_qs(parsed.query)
        try:
            if path == "/api/upload":
                return self.handle_upload(query)
            if path == "/api/transcribe":
                return self.handle_transcribe()
            return self.send_json(404, {"error": "not found"})
        except BrokenPipeError:
            pass
        except Exception as e:
            self.send_json(500, {"error": str(e)})

    def handle_upload(self, query: dict) -> None:
        name = sanitize_name((query.get("name") or [""])[0])
        track = (query.get("track") or [""])[0]
        ext = ((query.get("ext") or [""])[0]).lower()
        if not name:
            return self.send_json(400, {"error": "invalid or missing 'name'"})
        if track not in ("remote", "me"):
            return self.send_json(400, {"error": "track must be 'remote' or 'me'"})
        if ext not in UPLOAD_EXTS:
            return self.send_json(400, {"error": f"ext must be one of {sorted(UPLOAD_EXTS)}"})
        if (self.headers.get("Transfer-Encoding") or "").lower() == "chunked":
            self.close_connection = True
            return self.send_json(501, {"error": "chunked upload not supported"})
        try:
            size = int(self.headers.get("Content-Length", ""))
        except ValueError:
            return self.send_json(411, {"error": "Content-Length required"})
        if size > MAX_UPLOAD_BYTES:
            self.close_connection = True
            return self.send_json(413, {"error": f"upload exceeds MAX_UPLOAD_MB={config.MAX_UPLOAD_MB}"})

        target = safe_resolve(config.RECORDINGS_DIR, f"{name}-{track}.{ext}")
        if not target:
            return self.send_json(400, {"error": "invalid filename"})
        config.RECORDINGS_DIR.mkdir(parents=True, exist_ok=True)

        written = 0
        with open(target, "wb") as f:
            remaining = size
            while remaining > 0:
                chunk = self.rfile.read(min(CHUNK, remaining))
                if not chunk:
                    break
                f.write(chunk)
                written += len(chunk)
                remaining -= len(chunk)
        if written != size:
            self.close_connection = True        # body is out of sync; do not reuse
            return self.send_json(400, {"error": f"body shorter than Content-Length ({written}/{size})"})
        return self.send_json(200, {"saved": f"{config.RECORDINGS_DIR.name}/{target.name}", "bytes": written})

    def handle_transcribe(self) -> None:
        try:
            size = int(self.headers.get("Content-Length", ""))
        except ValueError:
            return self.send_json(411, {"error": "Content-Length required"})
        if size > 1024 * 1024:
            return self.send_json(413, {"error": "request body too large"})
        body = self.rfile.read(size)
        try:
            payload = json.loads(body or b"{}")
        except json.JSONDecodeError:
            return self.send_json(400, {"error": "invalid JSON body"})
        name = sanitize_name(str(payload.get("name", "")))
        language = str(payload.get("language", config.TRANSCRIBE_LANGUAGE)).strip().lower()
        if not name:
            return self.send_json(400, {"error": "invalid or missing 'name'"})
        if language not in transcriber.LANGUAGE_CODES:
            return self.send_json(400, {"error": f"language must be one of {sorted(transcriber.LANGUAGE_CODES)}"})
        status, response = start_job(name, language)
        return self.send_json(status, response)


def main():
    try:
        backend = config.resolve_backend()
    except SystemExit:
        backend = None
    server = ThreadingHTTPServer((config.HOST, config.PORT), Handler)

    def shutdown(signum, _frame):
        with JOBS_LOCK:
            child = CURRENT_CHILD
        if child:
            child.terminate()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGINT, shutdown)
    signal.signal(signal.SIGTERM, shutdown)

    print(f"zoom-taker server on http://{config.HOST}:{config.PORT} (backend: {backend or 'none installed'})")
    print(f"recordings: {config.RECORDINGS_DIR}  output: {config.OUTPUT_DIR}  model cache: {config.MODEL_CACHE_DIR}")
    server.serve_forever()


if __name__ == "__main__":
    main()
