package htx

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPaperLifecycleEvidenceFileStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper-evidence.jsonl")
	store := mustPaperLifecycleEvidenceFileStore(t, path)
	session := completedTestPaperLifecycleSession(t)

	committed, err := store.Append(session)
	if err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if !committed {
		t.Fatalf("Append committed = false, want true for first record")
	}

	restarted := mustPaperLifecycleEvidenceFileStore(t, path)
	records, err := restarted.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll after restart returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records after restart = %d, want 1", len(records))
	}

	expected, err := ExportPaperLifecycleEvidence(session)
	if err != nil {
		t.Fatalf("ExportPaperLifecycleEvidence returned error: %v", err)
	}
	if !reflect.DeepEqual(records[0], expected) {
		t.Fatalf("restarted record differs:\ngot=%#v\nwant=%#v", records[0], expected)
	}
}

func TestPaperLifecycleEvidenceFileStoreDeduplicatesRestartRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper-evidence.jsonl")
	store := mustPaperLifecycleEvidenceFileStore(t, path)
	session := completedTestPaperLifecycleSession(t)

	committed, err := store.Append(session)
	if err != nil {
		t.Fatalf("initial Append returned error: %v", err)
	}
	if !committed {
		t.Fatalf("initial Append committed = false, want true")
	}
	before := mustReadFile(t, path)

	restarted := mustPaperLifecycleEvidenceFileStore(t, path)
	committed, err = restarted.Append(session)
	if err != nil {
		t.Fatalf("retry Append returned error: %v", err)
	}
	if committed {
		t.Fatalf("retry Append committed = true, want false for exact duplicate")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("file changed after duplicate retry:\nbefore=%s\nafter=%s", string(before), string(after))
	}

	records, err := restarted.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records after duplicate retry = %d, want 1", len(records))
	}
}

func TestPaperLifecycleEvidenceFileStoreFailsClosedOnBadTail(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want string
	}{
		{
			name: "truncated tail",
			tail: `{"version":1`,
			want: "record boundary",
		},
		{
			name: "corrupt record",
			tail: `not-json` + "\n",
			want: "invalid character",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "paper-evidence.jsonl")
			store := mustPaperLifecycleEvidenceFileStore(t, path)
			if committed, err := store.Append(completedTestPaperLifecycleSession(t)); err != nil || !committed {
				t.Fatalf("initial Append committed=%t err=%v, want committed record", committed, err)
			}
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatalf("OpenFile append returned error: %v", err)
			}
			if _, err := file.WriteString(tc.tail); err != nil {
				_ = file.Close()
				t.Fatalf("WriteString tail returned error: %v", err)
			}
			if err := file.Close(); err != nil {
				t.Fatalf("Close returned error: %v", err)
			}
			before := mustReadFile(t, path)

			_, err = store.ReadAll()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReadAll error = %v, want %q", err, tc.want)
			}
			committed, err := store.Append(completedTestPaperLifecycleSession(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Append after bad tail committed=%t err=%v, want fail-closed read error %q", committed, err, tc.want)
			}
			if committed {
				t.Fatalf("Append after bad tail committed = true, want false")
			}
			if after := mustReadFile(t, path); !bytes.Equal(after, before) {
				t.Fatalf("file changed after append behind bad tail:\nbefore=%s\nafter=%s", string(before), string(after))
			}
		})
	}
}

func TestPaperLifecycleEvidenceFileStoreFailsClosedOnMissingRecordBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper-evidence.jsonl")
	store := mustPaperLifecycleEvidenceFileStore(t, path)
	if committed, err := store.Append(completedTestPaperLifecycleSession(t)); err != nil || !committed {
		t.Fatalf("initial Append committed=%t err=%v, want committed record", committed, err)
	}
	before := mustReadFile(t, path)
	if len(before) == 0 || before[len(before)-1] != '\n' {
		t.Fatalf("initial file does not end with newline: %q", string(before))
	}
	if err := os.Truncate(path, int64(len(before)-1)); err != nil {
		t.Fatalf("Truncate returned error: %v", err)
	}
	truncated := mustReadFile(t, path)

	_, err := store.ReadAll()
	if err == nil || !strings.Contains(err.Error(), "record boundary") {
		t.Fatalf("ReadAll error = %v, want record-boundary failure", err)
	}
	committed, err := store.Append(completedTestPaperLifecycleSession(t))
	if err == nil || !strings.Contains(err.Error(), "record boundary") {
		t.Fatalf("Append after missing boundary committed=%t err=%v, want record-boundary failure", committed, err)
	}
	if committed {
		t.Fatalf("Append after missing boundary committed = true, want false")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, truncated) {
		t.Fatalf("file changed after append behind missing boundary:\nbefore=%s\nafter=%s", string(truncated), string(after))
	}
}

func TestPaperLifecycleEvidenceFileStorePreservesCommittedRecordsOnAppendFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper-evidence.jsonl")
	store := mustPaperLifecycleEvidenceFileStore(t, path)
	if committed, err := store.Append(completedTestPaperLifecycleSession(t)); err != nil || !committed {
		t.Fatalf("initial Append committed=%t err=%v, want committed record", committed, err)
	}
	before := mustReadFile(t, path)

	oversize := completedTestPaperLifecycleSessionWithJSONLRecordSize(t, MaxPaperLifecycleEvidenceJSONLRecordBytes+1)
	committed, err := store.Append(oversize)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize Append committed=%t err=%v, want record-bound failure", committed, err)
	}
	if committed {
		t.Fatalf("oversize Append committed = true, want false")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("file changed after failed append:\nbefore=%s\nafter=%s", string(before), string(after))
	}

	records, err := store.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records after failed append = %d, want 1", len(records))
	}
}

func mustPaperLifecycleEvidenceFileStore(t *testing.T, path string) *PaperLifecycleEvidenceFileStore {
	t.Helper()

	store, err := NewPaperLifecycleEvidenceFileStore(path)
	if err != nil {
		t.Fatalf("NewPaperLifecycleEvidenceFileStore returned error: %v", err)
	}
	return store
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	return out
}
