package resource

import (
	"archive/zip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultDownloadTimeout = 2 * time.Minute

type DownloadOptions struct {
	ExpectedMD5 string
	Extract     bool
	ExtractDir  string
}

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

func (f *Fetcher) Download(ctx context.Context, url string, savePath string, options DownloadOptions) error {
	if err := os.MkdirAll(filepath.Dir(savePath), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", savePath, err)
	}

	if err := f.downloadToFile(ctx, url, savePath); err != nil {
		return err
	}

	if options.ExpectedMD5 != "" {
		match, err := hasExpectedMD5(savePath, options.ExpectedMD5)
		if err != nil {
			return err
		}
		if !match {
			return fmt.Errorf("md5 mismatch after download for %s", savePath)
		}
	}

	if options.Extract {
		if err := extractArchive(savePath, resolveExtractDir(savePath, options.ExtractDir)); err != nil {
			return err
		}
	}

	return nil
}

func (f *Fetcher) downloadToFile(ctx context.Context, url string, savePath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}

	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, resp.Status)
	}

	file, err := os.OpenFile(savePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", savePath, err)
	}

	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", savePath, err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", savePath, err)
	}

	return nil
}

func resolveExtractDir(savePath string, extractDir string) string {
	if extractDir != "" {
		return extractDir
	}
	return filepath.Dir(savePath)
}

func extractArchive(archivePath string, destinationDir string) error {
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return fmt.Errorf("create extract dir %s: %w", destinationDir, err)
	}

	switch strings.ToLower(filepath.Ext(archivePath)) {
	case ".zip":
		return extractZipArchive(archivePath, destinationDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", archivePath)
	}
}

func extractZipArchive(archivePath string, destinationDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", archivePath, err)
	}

	zr, err := zip.NewReader(file, info.Size())
	if err != nil {
		return fmt.Errorf("open zip archive %s: %w", archivePath, err)
	}

	for _, item := range zr.File {
		if err := extractZipFile(item, destinationDir); err != nil {
			return err
		}
	}

	return nil
}

func extractZipFile(file *zip.File, destinationDir string) error {
	targetPath := filepath.Join(destinationDir, file.Name)
	cleanDestDir := filepath.Clean(destinationDir)
	cleanTargetPath := filepath.Clean(targetPath)

	if cleanTargetPath != cleanDestDir && !strings.HasPrefix(cleanTargetPath, cleanDestDir+string(os.PathSeparator)) {
		return fmt.Errorf("invalid zip entry path %s", file.Name)
	}

	if file.FileInfo().IsDir() {
		if err := os.MkdirAll(cleanTargetPath, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", cleanTargetPath, err)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(cleanTargetPath), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", cleanTargetPath, err)
	}

	src, err := file.Open()
	if err != nil {
		return fmt.Errorf("open archive entry %s: %w", file.Name, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(cleanTargetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
	if err != nil {
		return fmt.Errorf("create %s: %w", cleanTargetPath, err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("extract %s: %w", cleanTargetPath, err)
	}

	return nil
}

func hasExpectedMD5(path string, expectedMD5 string) (bool, error) {
	actualMD5, err := fileMD5(path)
	if err != nil {
		return false, err
	}

	return strings.EqualFold(actualMD5, expectedMD5), nil
}

func fileMD5(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}

	// fmt.Println(hex.EncodeToString(hash.Sum(nil)))

	return hex.EncodeToString(hash.Sum(nil)), nil
}
