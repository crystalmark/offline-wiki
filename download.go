package main

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       60 * time.Second,
	},
}

const userAgent = "offline-wiki-updater/1.0"

func getText(ctx context.Context, url string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	ctx2, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	resp, err := httpClient.Do(req.WithContext(ctx2))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return string(b), err
}

// remoteSize returns the Content-Length of url after redirects.
func remoteSize(ctx context.Context, url string) (int64, error) {
	req, _ := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HEAD %s: HTTP %d", url, resp.StatusCode)
	}
	return resp.ContentLength, nil
}

// stallReader cancels the request if no data arrives for a while.
type stallReader struct {
	r      io.Reader
	timer  *time.Timer
	window time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(s.window)
	}
	return n, err
}

// download fetches url to dst, resuming from dst+".part" and retrying on
// network errors. It reports progress on stdout.
func download(ctx context.Context, url, dst, label string) error {
	part := dst + ".part"
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= 8; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		done, err := downloadOnce(ctx, url, part, label)
		if err == nil && done {
			return os.Rename(part, dst)
		}
		lastErr = err
		wait := time.Duration(attempt*attempt) * 5 * time.Second
		fmt.Fprintf(uout, "  %s: %v - retrying in %s\n", label, err, wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("%s: giving up: %w", label, lastErr)
}

func downloadOnce(ctx context.Context, url, part, label string) (bool, error) {
	var have int64
	if fi, err := os.Stat(part); err == nil {
		have = fi.Size()
	}
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx2, "GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var total int64
	flags := os.O_CREATE | os.O_WRONLY
	switch resp.StatusCode {
	case 206:
		flags |= os.O_APPEND
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			if i := strings.LastIndex(cr, "/"); i >= 0 {
				total, _ = strconv.ParseInt(cr[i+1:], 10, 64)
			}
		}
	case 200:
		flags |= os.O_TRUNC
		have = 0
		total = resp.ContentLength
	case 416:
		// Range not satisfiable: we already have the whole file.
		return true, nil
	default:
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()

	window := 2 * time.Minute
	timer := time.AfterFunc(window, cancel)
	defer timer.Stop()
	src := &stallReader{r: resp.Body, timer: timer, window: window}

	buf := make([]byte, 1<<20)
	got := have
	start, lastPrint := time.Now(), time.Now()
	startHave := have
	for {
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return false, werr
			}
			got += int64(n)
		}
		if time.Since(lastPrint) > 10*time.Second {
			lastPrint = time.Now()
			rate := float64(got-startHave) / time.Since(start).Seconds()
			if total > 0 {
				eta := time.Duration(float64(total-got)/rate) * time.Second
				fmt.Fprintf(uout, "  %s: %.1f%% of %s, %s/s, about %s left\n", label, 100*float64(got)/float64(total), human(total), human(int64(rate)), eta.Round(time.Minute))
			} else {
				fmt.Fprintf(uout, "  %s: %s, %s/s\n", label, human(got), human(int64(rate)))
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return false, rerr
		}
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	if total > 0 && got != total {
		return false, fmt.Errorf("incomplete: %d of %d bytes", got, total)
	}
	return true, nil
}

func human(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0f KB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}

// checksum verifies path against a hex digest (sha256 or md5 by length).
func checksum(path, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	var h hash.Hash
	switch len(want) {
	case 64:
		h = sha256.New()
	case 32:
		h = md5.New()
	default:
		return fmt.Errorf("unrecognised checksum %q", want)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.CopyBuffer(h, f, make([]byte, 4<<20)); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("checksum mismatch for %s", filepath.Base(path))
	}
	return nil
}

// fetchDigest reads a "<hex>  <name>" checksum file.
func fetchDigest(ctx context.Context, url string) (string, error) {
	t, err := getText(ctx, url)
	if err != nil {
		return "", err
	}
	f := strings.Fields(t)
	if len(f) == 0 {
		return "", fmt.Errorf("empty checksum file %s", url)
	}
	return f[0], nil
}

func writeBytes(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}
