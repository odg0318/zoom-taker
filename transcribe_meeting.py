#!/usr/bin/env python3
"""Track files under recordings/<name>-* -> speaker-labeled transcript markdown.

Usage:
    python transcribe_meeting.py <name> [--language kr|en|cn]

Supported tracks (the suffix decides the speaker):
  -system.caf / -remote.webm  -> REMOTE (audiorec / browser tab recording)
  -mic.caf    / -me.webm      -> ME     (audiorec / browser microphone recording)

One missing track is fine (single-track transcription); both missing is an error.

Per-track Whisper transcription, then:
1. Drop segments overlapping silence (ffmpeg silencedetect) — hallucinations on silence
2. Drop consecutive repeat loops (same normalized text in a row)
3. Drop timestamps beyond the audio duration (track drift)
4. Drop quiet segments (RMS energy far below the track's speech peak)
5. Attribute mic segments overlapping system segments in time+text to REMOTE (speaker bleed)
"""
import argparse
import difflib
import math
import pathlib
import re
import subprocess
import warnings

import config
import transcriber

SIMILARITY_THRESHOLD = 0.6   # mic/system text similarity above this -> bleed
TIME_WINDOW_S = 3.0          # bleed detection time window
SILENCE_NOISE_DB = -45       # silencedetect threshold
SILENCE_MIN_DUR = 1.5        # silences at least this long count

SILENCE_OVERLAP_MAX = 0.5    # segment at least this fraction inside silence -> hallucination
QUIET_DROP_DB = 15.0         # this far below the track's speech peak -> hallucination
QUIET_FLOOR_DB = -55.0       # absolute floor: always drop below this


def ffprobe_duration(path: pathlib.Path) -> float:
    out = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", str(path)],
        capture_output=True, text=True, check=True,
    ).stdout.strip()
    try:
        return float(out)
    except ValueError:
        pass
    # Chrome MediaRecorder webm files omit duration in the header -> full decode
    proc = subprocess.run(
        ["ffmpeg", "-i", str(path), "-f", "null", "-"],
        capture_output=True, text=True,
    )
    stamps = re.findall(r"time=(\d+):(\d+):([\d.]+)", proc.stderr)
    if not stamps:
        raise SystemExit(f"Failed to measure duration: {path}")
    h, m, s = stamps[-1]
    return int(h) * 3600 + int(m) * 60 + float(s)


def silent_ranges(path: pathlib.Path) -> list[tuple[float, float]]:
    proc = subprocess.run(
        ["ffmpeg", "-i", str(path),
         "-af", f"silencedetect=noise={SILENCE_NOISE_DB}dB:d={SILENCE_MIN_DUR}",
         "-f", "null", "-"],
        capture_output=True, text=True,
    )
    starts = [float(m) for m in re.findall(r"silence_start: ([\d.]+)", proc.stderr)]
    ends = [float(m) for m in re.findall(r"silence_end: ([\d.]+)", proc.stderr)]
    # silence_end is omitted when the last silence runs to end of file
    return list(zip(starts, ends + [ffprobe_duration(path)] * (len(starts) - len(ends))))


def silence_overlap(seg: dict, duration: float, ranges: list[tuple[float, float]]) -> float:
    """Fraction of a segment overlapping silence (timestamp drift clipped by duration)."""
    start = min(seg["start"], duration)
    end = min(seg["end"], duration)
    inter = sum(max(0.0, min(end, e) - max(start, s)) for s, e in ranges)
    return inter / max(end - start, 1e-6)


def normalize(text: str) -> str:
    return re.sub(r"[\s.,!?~'\"…]+", "", text)


def _rms_s16(pcm: bytes) -> int:
    """RMS of 16-bit mono PCM samples. audioop is gone in Python 3.13, so
    fall back to a plain array-based computation with the same math."""
    try:
        with warnings.catch_warnings():       # deprecation warning on 3.11/3.12
            warnings.simplefilter("ignore", DeprecationWarning)
            import audioop
        return audioop.rms(pcm, 2)
    except ImportError:
        import array
        samples = array.array("h")
        samples.frombytes(pcm[: len(pcm) // 2 * 2])   # ignore a trailing odd byte
        if not samples:
            return 0
        acc = sum(v * v for v in samples)
        return int(math.sqrt(acc / len(samples)))


def pcm_rms_db(path: pathlib.Path, start: float, end: float) -> float:
    """RMS of [start, end) in dBFS (-ac 1 mixes channels down to mono)."""
    proc = subprocess.run(
        ["ffmpeg", "-v", "error", "-i", str(path), "-f", "s16le", "-acodec", "pcm_s16le",
         "-ac", "1", "-ss", f"{start:.3f}", "-t", f"{end - start:.3f}", "-"],
        capture_output=True,
    )
    rms = _rms_s16(proc.stdout)
    return 20 * math.log10(rms / 32768.0) if rms else -120.0


def clean(segments: list[dict], path: pathlib.Path, duration: float,
          silence: list[tuple[float, float]]) -> list[dict]:
    kept, prev_norm, peak_db = [], None, -120.0
    stages = []
    for seg in segments:
        if seg["start"] >= duration:              # timestamp drift past the end
            continue
        if silence_overlap(seg, duration, silence) >= SILENCE_OVERLAP_MAX:
            continue                              # hallucination on silence
        norm = normalize(seg["text"])
        if norm and norm == prev_norm:            # consecutive repeat loop
            continue
        kept.append(seg)
        prev_norm = norm
    for seg in kept:                              # reference speech level of the track
        peak_db = max(peak_db, pcm_rms_db(path, seg["start"], seg["end"]))
    survived = []
    for seg in kept:
        db = pcm_rms_db(path, seg["start"], seg["end"])
        if db < QUIET_FLOOR_DB or db < peak_db - QUIET_DROP_DB:
            stages.append(f"    drop(quiet {db:.0f}dB < peak {peak_db:.0f}dB): {seg['text'].strip()[:30]}")
            continue
        survived.append(seg)
    for line in stages:
        print(line)
    return survived


def merge(remote: list[dict], me: list[dict]) -> list[dict]:
    """Mic segments overlapping system segments in time+text are bleed: drop and attribute to REMOTE."""
    out = [{"speaker": "REMOTE", **s} for s in remote]
    for seg in me:
        norm = normalize(seg["text"])
        bleed = any(
            abs(seg["start"] - r["start"]) < TIME_WINDOW_S
            and difflib.SequenceMatcher(None, norm, normalize(r["text"])).ratio() > SIMILARITY_THRESHOLD
            for r in remote
        )
        if not bleed:
            out.append({"speaker": "ME", **seg})
    return sorted(out, key=lambda s: s["start"])


def fmt(t: float) -> str:
    return f"{int(t) // 60:02d}:{int(t) % 60:02d}"


TRACK_SUFFIXES = {"REMOTE": ("system", "remote"), "ME": ("mic", "me")}
TRACK_EXTS = (".caf", ".webm", ".m4a", ".wav")


def find_track(name: str, suffix: str) -> pathlib.Path | None:
    for ext in TRACK_EXTS:
        path = config.RECORDINGS_DIR / f"{name}-{suffix}{ext}"
        if path.exists():
            return path
    return None


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("name", help="track prefix under recordings/ (e.g. zoom-live)")
    parser.add_argument("--language", default=config.TRANSCRIBE_LANGUAGE,
                        help=f"meeting language: {', '.join(transcriber.LANGUAGE_CODES)} or a combo like kr+en (default: %(default)s)")
    args = parser.parse_args()

    tracks = {}
    for label, suffixes in TRACK_SUFFIXES.items():
        path = None
        for suffix in suffixes:
            path = find_track(args.name, suffix)
            if path:
                break
        if not path:
            continue
        duration = ffprobe_duration(path)
        silence = silent_ranges(path)
        segments = clean(transcriber.transcribe(path, language=args.language), path, duration, silence)
        print(f"{path.name}: {len(segments)} segments kept (audio {duration:.1f}s, silence {len(silence)} ranges)")
        tracks[label] = segments

    if not tracks:
        raise SystemExit(
            f"No track files found for '{args.name}' — expected "
            f"recordings/{args.name}-{{system|remote|mic|me}}{{.caf|.webm|.m4a|.wav}}"
        )
    missing = sorted(set(TRACK_SUFFIXES) - set(tracks))
    if missing:
        print(f"warning: no {'/'.join(missing)} track found — transcribing without it")

    merged = merge(tracks.get("REMOTE", []), tracks.get("ME", []))

    config.OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    out_path = config.OUTPUT_DIR / f"{args.name}-transcript.md"
    with open(out_path, "w") as f:
        f.write(f"# {args.name} transcript\n\n")
        for seg in merged:
            f.write(f"[{fmt(seg['start'])}] {seg['speaker']}: {seg['text'].strip()}\n")
    print(f"Transcript saved: {out_path} ({len(merged)} segments)")


if __name__ == "__main__":
    main()
