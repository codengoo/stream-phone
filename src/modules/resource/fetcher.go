package resource

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const DefaultDownloadTimeout = 2 * time.Minute

type Fetcher struct {
	HTTPClient *http.Client
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		HTTPClient: &http.Client{
			Timeout: DefaultDownloadTimeout,
		},
	}
}

func (f *Fetcher) EnsureFile(ctx context.Context, path string, download func(context.Context) ([]byte, error)) error {
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}

	content, err := download(ctx)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, content, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func (f *Fetcher) Download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build download request: %w", err)
	}

	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read download body from %s: %w", url, err)
	}

	return content, nil
}
