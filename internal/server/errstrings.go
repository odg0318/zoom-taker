package server

// Wire-compatible error strings, byte-for-byte from server.py — the recorder
// page surfaces some of them verbatim and the wire-parity tests lock them in.
const (
	errNotFound         = "not found"
	errInvalidName      = "invalid or missing 'name'"
	errTrack            = "track must be 'remote' or 'me'"
	errExt              = "ext must be one of ['caf', 'm4a', 'wav', 'webm']" // Python sorted(UPLOAD_EXTS) repr
	errChunked          = "chunked upload not supported"
	errLengthRequired   = "Content-Length required"
	errBodyTooLarge     = "request body too large"
	errBadJSON          = "invalid JSON body"
	errBadLanguage      = "language must be kr, en, cn, or a combo like kr+en"
	errBusy             = "a transcription is already running"
	errUnknownJob       = "unknown job"
	errInvalidFile      = "invalid file name"
	errInvalidFilename  = "invalid filename"
	errPageMissing      = "record-zoom.html missing"
	errTooLargeUnquoted = "upload exceeds MAX_UPLOAD_MB=%d" // value interpolated at send time
	errShortBody        = "body shorter than Content-Length (%d/%d)"
)

// errorPayload shapes {"error": ...} — job responses add the "job" key.
type errorPayload struct {
	Error string `json:"error"`
}

type busyPayload struct {
	Error string `json:"error"`
	Job   string `json:"job"`
}
