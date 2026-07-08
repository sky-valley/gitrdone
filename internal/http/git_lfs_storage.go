package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type lfsObjectStore interface {
	Exists(ctx context.Context, repoID string, oid string, size int64) (bool, error)
	Put(ctx context.Context, repoID string, oid string, size int64, maxSize int64, body io.Reader) error
	Open(ctx context.Context, repoID string, oid string) (lfsStoredObject, error)
}

type lfsStoredObject struct {
	Size int64
	Body io.ReadCloser
}

var errLFSObjectNotFound = errors.New("lfs object not found")
var errLFSObjectInvalid = errors.New("lfs object invalid")
var errLFSObjectTooLarge = errors.New("lfs object too large")

type filesystemLFSObjectStore struct {
	root string
}

func newFilesystemLFSObjectStore(root string) filesystemLFSObjectStore {
	root = strings.TrimSpace(root)
	if root == "" {
		root = defaultGitStorageRoot
	}
	return filesystemLFSObjectStore{root: root}
}

func (store filesystemLFSObjectStore) Exists(ctx context.Context, repoID string, oid string, size int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Stat(store.objectPath(repoID, oid))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, nil
	}
	return true, nil
}

func (store filesystemLFSObjectStore) Put(ctx context.Context, repoID string, oid string, size int64, maxSize int64, body io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	maxSize = normalizeMaxLFSObjectBytes(maxSize)
	if size > maxSize {
		return errLFSObjectTooLarge
	}
	objectPath := store.objectPath(repoID, oid)
	objectDir := filepath.Dir(objectPath)
	if err := os.MkdirAll(objectDir, 0o700); err != nil {
		return err
	}

	temp, err := os.CreateTemp(objectDir, ".upload-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.Remove(tempPath)
		}
	}()

	hash := sha256.New()
	limited := &io.LimitedReader{R: body, N: maxSize + 1}
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), limited)
	closeErr := temp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxSize {
		return errLFSObjectTooLarge
	}
	if written != size {
		return fmt.Errorf("%w: size mismatch", errLFSObjectInvalid)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != oid {
		return fmt.Errorf("%w: sha256 mismatch", errLFSObjectInvalid)
	}
	if err := os.Rename(tempPath, objectPath); err != nil {
		return err
	}
	keepTemp = true
	return nil
}

func (store filesystemLFSObjectStore) Open(ctx context.Context, repoID string, oid string) (lfsStoredObject, error) {
	if err := ctx.Err(); err != nil {
		return lfsStoredObject{}, err
	}
	objectPath := store.objectPath(repoID, oid)
	file, err := os.Open(objectPath)
	if errors.Is(err, os.ErrNotExist) {
		return lfsStoredObject{}, errLFSObjectNotFound
	}
	if err != nil {
		return lfsStoredObject{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return lfsStoredObject{}, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return lfsStoredObject{}, errLFSObjectNotFound
	}
	return lfsStoredObject{Size: info.Size(), Body: file}, nil
}

func (store filesystemLFSObjectStore) objectPath(repoID string, oid string) string {
	return filepath.Join(store.root, "lfs", repoID, "objects", oid[0:2], oid[2:4], oid)
}
