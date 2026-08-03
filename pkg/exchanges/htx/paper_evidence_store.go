package htx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type PaperLifecycleEvidenceFileStore struct {
	path string
	lock *sync.Mutex
}

// PaperLifecycleEvidenceFileStore serializes append/read operations per canonical path within this process.
// It does not provide cross-process file locking; callers running multiple processes need external serialization.
func NewPaperLifecycleEvidenceFileStore(path string) (*PaperLifecycleEvidenceFileStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("HTX paper lifecycle evidence file path is empty")
	}
	canonical, err := canonicalPaperLifecycleEvidenceFilePath(path)
	if err != nil {
		return nil, err
	}
	return &PaperLifecycleEvidenceFileStore{
		path: canonical,
		lock: paperLifecycleEvidenceFileStoreLock(canonical),
	}, nil
}

func (s *PaperLifecycleEvidenceFileStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *PaperLifecycleEvidenceFileStore) Append(session *PaperLifecycleSession) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("HTX paper lifecycle evidence file store is nil")
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	return s.appendLocked(session)
}

func (s *PaperLifecycleEvidenceFileStore) appendLocked(session *PaperLifecycleSession) (bool, error) {
	records, err := s.readAllLocked()
	if err != nil {
		return false, err
	}

	evidence, err := ExportPaperLifecycleEvidence(session)
	if err != nil {
		return false, err
	}
	line, err := marshalPaperLifecycleEvidenceJSONLRecord(evidence)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		existing, err := marshalPaperLifecycleEvidenceJSONLRecord(record)
		if err != nil {
			return false, err
		}
		if bytes.Equal(existing, line) {
			return false, nil
		}
	}

	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer file.Close()

	sink, err := newPaperLifecycleEvidenceFileSink(file)
	if err != nil {
		return false, err
	}
	if err := appendPaperLifecycleEvidenceJSONLRecord(sink, line); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PaperLifecycleEvidenceFileStore) ReadAll() ([]PaperLifecycleEvidence, error) {
	if s == nil {
		return nil, fmt.Errorf("HTX paper lifecycle evidence file store is nil")
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	return s.readAllLocked()
}

func (s *PaperLifecycleEvidenceFileStore) readAllLocked() ([]PaperLifecycleEvidence, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []PaperLifecycleEvidence{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if err := validatePaperLifecycleEvidenceFileBoundary(file); err != nil {
		return nil, err
	}
	return ReadPaperLifecycleEvidenceJSONL(file)
}

var paperLifecycleEvidenceFileStoreLocks sync.Map

func paperLifecycleEvidenceFileStoreLock(path string) *sync.Mutex {
	lock, _ := paperLifecycleEvidenceFileStoreLocks.LoadOrStore(path, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func canonicalPaperLifecycleEvidenceFilePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, base), nil
	}
	return filepath.Clean(abs), nil
}

func validatePaperLifecycleEvidenceFileBoundary(file *os.File) error {
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() == 0 {
		return nil
	}

	if _, err := file.Seek(-1, io.SeekEnd); err != nil {
		return err
	}
	var last [1]byte
	if _, err := file.Read(last[:]); err != nil {
		return err
	}
	if last[0] != '\n' {
		return fmt.Errorf("HTX paper lifecycle evidence file does not end at a committed JSONL record boundary")
	}
	_, err = file.Seek(0, io.SeekStart)
	return err
}

type paperLifecycleEvidenceFileSink struct {
	file *os.File
	pos  int64
}

func newPaperLifecycleEvidenceFileSink(file *os.File) (*paperLifecycleEvidenceFileSink, error) {
	if file == nil {
		return nil, fmt.Errorf("HTX paper lifecycle evidence file is nil")
	}
	pos, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	return &paperLifecycleEvidenceFileSink{file: file, pos: pos}, nil
}

func (s *paperLifecycleEvidenceFileSink) Len() int {
	return int(s.pos)
}

func (s *paperLifecycleEvidenceFileSink) Truncate(n int) error {
	pos := int64(n)
	if err := s.file.Truncate(pos); err != nil {
		return err
	}
	if _, err := s.file.Seek(pos, io.SeekStart); err != nil {
		return err
	}
	s.pos = pos
	return nil
}

func (s *paperLifecycleEvidenceFileSink) Write(p []byte) (int, error) {
	n, err := s.file.Write(p)
	s.pos += int64(n)
	return n, err
}
