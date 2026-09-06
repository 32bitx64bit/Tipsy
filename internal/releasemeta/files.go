package releasemeta

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

var metadataNamePattern = regexp.MustCompile(`^(?:[1-9][0-9]*\.)?(?:root|targets|snapshot|timestamp|stable|beta|recovery|roblox-policy|runtime-policy)\.json$`)

type localFetcher struct {
	dir   string
	mu    sync.Mutex
	reads map[string][]byte
}

func newLocalFetcher(dir string) (*localFetcher, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fail(ReasonUnsafePath, "metadata directory is not a real directory")
	}
	return &localFetcher{dir: absolute, reads: map[string][]byte{}}, nil
}

func (f *localFetcher) DownloadFile(rawURL string, maxLength int64, _ time.Duration) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "local.tipsy.invalid" || !strings.HasPrefix(parsed.EscapedPath(), "/metadata/") {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusBadRequest, URL: rawURL}
	}
	escaped := strings.TrimPrefix(parsed.EscapedPath(), "/metadata/")
	name, err := url.PathUnescape(escaped)
	if err != nil || strings.Contains(name, "/") || !metadataNamePattern.MatchString(name) {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusBadRequest, URL: rawURL}
	}
	data, err := readNamedNoFollow(f.dir, name, maxLength)
	if os.IsNotExist(err) {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusNotFound, URL: rawURL}
	}
	if err != nil {
		return nil, err
	}
	if err := checkJSONDepth(data, MaxMetadataDepth); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.reads[name] = append([]byte(nil), data...)
	f.mu.Unlock()
	return data, nil
}

func (f *localFetcher) read(name string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.reads[name]...)
}

func readNamedNoFollow(dir, name string, maximum int64) ([]byte, error) {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return nil, fail(ReasonUnsafePath, "unsafe filename")
	}
	fd, err := syscall.Open(filepath.Join(dir, name), syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: fmt.Sprintf("%s exceeds bounded regular-file policy", name)}
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: fmt.Sprintf("%s exceeds %d bytes", name, maximum)}
	}
	return data, nil
}

func readTargetBeneath(dir, target string, maximum int64) ([]byte, string, error) {
	if err := validateTargetPath(target, strings.Split(target, "/")[0]+"/"); err != nil {
		return nil, "", err
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	baseFD, err := syscall.Open(absolute, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	current := baseFD
	defer func() { _ = syscall.Close(current) }()
	parts := strings.Split(target, "/")
	for _, part := range parts[:len(parts)-1] {
		next, openErr := syscall.Openat(current, part, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, "", openErr
		}
		_ = syscall.Close(current)
		current = next
	}
	fd, err := syscall.Openat(current, parts[len(parts)-1], syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	file := os.NewFile(uintptr(fd), parts[len(parts)-1])
	defer file.Close()
	return readAndDigest(file, maximum)
}

func readArtifactNoFollow(name string, maximum int64) ([]byte, string, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, "", err
	}
	fd, err := syscall.Open(absolute, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(absolute))
	defer file.Close()
	return readAndDigest(file, maximum)
}

func hashArtifactNoFollow(name string, maximum int64) (string, int64, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return "", 0, err
	}
	fd, err := syscall.Open(absolute, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", 0, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(absolute))
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || info.Mode().Perm()&0022 != 0 || info.Size() <= 0 || info.Size() > maximum {
		return "", 0, fail(ReasonUnsafePath, "artifact is not a bounded, single-link, non-writable regular file")
	}
	hash := sha256.New()
	written, err := io.CopyBuffer(hash, io.LimitReader(file, maximum+1), make([]byte, 128<<10))
	if err != nil {
		return "", 0, err
	}
	if written != info.Size() || written > maximum {
		return "", 0, fail(ReasonArtifactMismatch, "artifact changed while hashing or exceeded its limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), written, nil
}

func readAndDigest(file *os.File, maximum int64) ([]byte, string, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || info.Mode().Perm()&0022 != 0 || info.Size() < 0 || info.Size() > maximum {
		return nil, "", fail(ReasonUnsafePath, "file is not a bounded, single-link, non-writable regular file")
	}
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var data []byte
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			if int64(len(data)+n) > maximum {
				return nil, "", fail(ReasonMetadataTooLarge, "file exceeds %d bytes", maximum)
			}
			_, _ = hash.Write(buffer[:n])
			data = append(data, buffer[:n]...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, "", readErr
		}
	}
	if int64(len(data)) != info.Size() {
		return nil, "", fail(ReasonArtifactMismatch, "file changed while reading")
	}
	return data, hex.EncodeToString(hash.Sum(nil)), nil
}
