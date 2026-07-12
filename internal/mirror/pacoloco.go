package mirror

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// pacolocoVersion pins the upstream release we install. Bump as needed.
const pacolocoVersion = "v1.7"

// pacolocoReleaseURL is the official release tarball location. Pacoloco
// distributes static linux-amd64 / linux-arm64 binaries inside .tar.gz archives
// produced by goreleaser.
//
// Layout inside the archive:
//
//	pacoloco
//	LICENSE
//	README.md
//	example.pacoloco.yaml
func pacolocoReleaseURL() (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("pacoloco mirror is only supported on Linux")
	}
	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported arch %q for pacoloco", runtime.GOARCH)
	}
	return fmt.Sprintf(
		"https://github.com/anatol/pacoloco/releases/download/%s/pacoloco-%s-linux-%s.tar.gz",
		pacolocoVersion, pacolocoVersion, arch,
	), nil
}

// EnsureBinary downloads and extracts the pacoloco binary into p.BinPath if it
// is not already present. Returns the absolute path to the binary.
func EnsureBinary(ctx context.Context, p *Paths) (string, error) {
	if _, err := os.Stat(p.BinPath); err == nil {
		return p.BinPath, nil
	}
	if err := os.MkdirAll(p.BinDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", p.BinDir, err)
	}

	url, err := pacolocoReleaseURL()
	if err != nil {
		return "", err
	}

	fmt.Fprintf(os.Stderr, "pacoloco not found — downloading %s…\n", pacolocoVersion)

	tarPath := filepath.Join(os.TempDir(), fmt.Sprintf("pacoloco-%s.tar.gz", pacolocoVersion))
	if err := downloadFile(ctx, tarPath, url); err != nil {
		return "", fmt.Errorf("download pacoloco: %w", err)
	}
	defer func() { _ = os.Remove(tarPath) }()

	if err := extractPacolocoBinary(tarPath, p.BinPath); err != nil {
		return "", fmt.Errorf("extract pacoloco: %w", err)
	}
	//nolint:gosec // G302: pacoloco is an executable; the owner exec bit is required to run it. Owner-only 0o700.
	if err := os.Chmod(p.BinPath, 0o700); err != nil {
		return "", fmt.Errorf("chmod pacoloco: %w", err)
	}
	fmt.Fprintf(os.Stderr, "pacoloco installed: %s\n", p.BinPath)
	return p.BinPath, nil
}

func downloadFile(ctx context.Context, dst, url string) error {
	hc := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, url)
	}
	//nolint:gosec // G304: dst is an internally-derived temp path, not user input.
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractPacolocoBinary opens a .tar.gz at src and writes the "pacoloco" entry
// to dst.
func extractPacolocoBinary(src, dst string) error {
	//nolint:gosec // G304: src is an internally-derived temp path, not user input.
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("pacoloco binary not found in archive")
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != "pacoloco" {
			continue
		}
		//nolint:gosec // G304: dst is an internally-derived install path, not user input.
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		// Cap the copy to guard against a decompression bomb (gosec G110). The
		// pacoloco binary is a few tens of MB; 512 MiB is a generous ceiling.
		const maxBinarySize = 512 << 20
		n, err := io.Copy(out, io.LimitReader(tr, maxBinarySize))
		if err != nil {
			_ = out.Close()
			return err
		}
		if n == maxBinarySize {
			_ = out.Close()
			return fmt.Errorf("pacoloco binary exceeds %d bytes limit", int64(maxBinarySize))
		}
		return out.Close()
	}
}
