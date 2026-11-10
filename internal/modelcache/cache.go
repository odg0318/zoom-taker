// Package modelcache resolves GGML model files against a local cache dir,
// downloading them from the ggerganov/whisper.cpp HuggingFace repo when
// missing. Downloads honor HF_ENDPOINT (mirror/proxy) and HF_TOKEN, and
// resume partial transfers; once the file is cached everything runs offline.
package modelcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const MiB = 1 << 20

// metaFile records what was downloaded so cache validity can be checked
// offline (size match) without contacting the network.
type metaFile struct {
	Repo      string `json:"repo"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Completed string `json:"completed"`
}

// Resolve returns a local path for spec. spec is either a path to an existing
// file (used as-is) or a GGML file name inside the HF repo, resolved under
// cacheDir and downloaded if missing. log receives progress lines.
func Resolve(ctx context.Context, spec, cacheDir, endpoint, token string, log func(string)) (string, error) {
	if spec == "" {
		return "", errors.New("model not specified")
	}
	// A path (contains a separator or exists on disk) is used verbatim.
	if strings.ContainsRune(spec, '/') || strings.ContainsRune(spec, filepath.Separator) {
		if st, err := os.Stat(spec); err == nil && !st.IsDir() {
			return spec, nil
		}
		return "", fmt.Errorf("model file not found: %s", spec)
	}
	if !strings.HasSuffix(spec, ".bin") {
		return "", fmt.Errorf("model must be a GGML .bin file name or an existing path, got %q", spec)
	}

	bin := filepath.Join(cacheDir, spec)
	if ok := cached(bin, log); ok {
		return bin, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	if err := download(ctx, spec, bin, endpoint, token, log); err != nil {
		return "", err
	}
	return bin, nil
}

// cached reports whether bin is a complete download. Offline-first: any
// match short-circuits before any network activity.
func cached(bin string, log func(string)) bool {
	st, err := os.Stat(bin)
	if err != nil || st.IsDir() || st.Size() == 0 {
		return false
	}
	var m metaFile
	raw, err := os.ReadFile(bin[:len(bin)-4] + ".json")
	if err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	switch {
	case m.Size > 0 && m.Size == st.Size():
		return true
	case m.Size == 0:
		// No meta (e.g. hand-copied file): accept non-empty, warn loudly.
		emit(log, fmt.Sprintf("warning: %s has no meta — accepting %dMiB file as-is", filepath.Base(bin), st.Size()/MiB))
		return true
	default:
		emit(log, fmt.Sprintf("warning: cached %s is %d bytes, expected %d — redownloading", filepath.Base(bin), st.Size(), m.Size))
		return false
	}
}

func download(ctx context.Context, name, bin, endpoint, token string, log func(string)) error {
	part := bin[:len(bin)-4] + ".part"
	metaPath := bin[:len(bin)-4] + ".json"

	repo := "ggerganov/whisper.cpp"
	base := strings.TrimRight(endpoint, "/")
	if base == "" {
		base = "https://huggingface.co"
	}
	u := base + "/" + repo + "/resolve/main/" + url.PathEscape(name)

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
		emit(log, fmt.Sprintf("model download: resuming from %dMiB", offset/MiB))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("model download: %w (set HF_ENDPOINT if huggingface.co is unreachable)", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent:
		// appending to the existing .part
	case resp.StatusCode == http.StatusOK && offset > 0:
		// server ignored the range; restart from scratch
		emit(log, "model download: server ignored resume range — restarting")
		offset = 0
	case resp.StatusCode == http.StatusOK:
		// fresh download
	default:
		return fmt.Errorf("model download: HTTP %d for %s", resp.StatusCode, name)
	}

	total := resp.ContentLength + offset // -1 when unknown (rare even for ranges)
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	if offset > 0 {
		// hash the already-downloaded prefix so the final digest covers the file
		if pf, err := os.Open(part); err == nil {
			_, _ = io.Copy(hasher, pf) //nolint — best effort; prefix is intact
			pf.Close()
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return err
		}
	}

	written := offset
	var next int64 // next 10% milestone, in bytes
	if total > 0 {
		next = total / 10
	}
	buf := make([]byte, 512*1024)
	hashW := io.MultiWriter(f, hasher)
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return fmt.Errorf("model download cancelled: %w", err)
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := hashW.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			written += int64(n)
			if total > 0 && written >= next && next > 0 {
				emit(log, fmt.Sprintf("model download: %d%% (%dMiB/%dMiB)", written*100/total, written/MiB, total/MiB))
				for next <= written {
					next += total / 10
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("model download: %w", rerr)
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if total > 0 && written != total {
		return fmt.Errorf("model download: got %d bytes, expected %d", written, total)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	m := metaFile{Repo: repo, Size: written, SHA256: sum, Completed: "resumed-or-fresh"}
	if raw, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(metaPath, raw, 0o644) //nolint — meta is advisory
	}
	if err := os.Rename(part, bin); err != nil {
		return err
	}
	emit(log, fmt.Sprintf("model ready: %s (%dMiB, sha256 %s…)", name, written/MiB, sum[:12]))
	return nil
}

func emit(log func(string), line string) {
	if log != nil {
		log(line)
	}
}
