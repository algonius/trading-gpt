package htx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func TestPaperLifecycleEvidenceFileStoreConcurrentDuplicateAppendHasOneCommitter(t *testing.T) {
	const workers = 16
	path := filepath.Join(t.TempDir(), "paper-evidence.jsonl")
	sessions := make([]*PaperLifecycleSession, workers)
	for i := range sessions {
		sessions[i] = completedTestPaperLifecycleSession(t)
	}

	results := runConcurrentPaperEvidenceStoreAppends(t, path, sessions)
	committers := 0
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("worker %d Append returned error: %v", i, result.err)
		}
		if result.committed {
			committers++
		}
	}
	if committers != 1 {
		t.Fatalf("successful committers = %d, want 1", committers)
	}

	records, err := mustPaperLifecycleEvidenceFileStore(t, path).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records after concurrent duplicate append = %d, want 1", len(records))
	}
}

func TestPaperLifecycleEvidenceFileStoreConcurrentDistinctAppendRetainsEveryRecord(t *testing.T) {
	const workers = 16
	path := filepath.Join(t.TempDir(), "paper-evidence.jsonl")
	sessions := make([]*PaperLifecycleSession, workers)
	wantClientOrderIDs := make(map[string]struct{}, workers)
	for i := range sessions {
		clientOrderID := fmt.Sprintf("buy-open-%02d", i)
		sessions[i] = completedTestPaperLifecycleSessionWithFirstClientOrderID(t, clientOrderID)
		wantClientOrderIDs[clientOrderID] = struct{}{}
	}

	results := runConcurrentPaperEvidenceStoreAppends(t, path, sessions)
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("worker %d Append returned error: %v", i, result.err)
		}
		if !result.committed {
			t.Fatalf("worker %d Append committed = false, want true for distinct evidence", i)
		}
	}

	records, err := mustPaperLifecycleEvidenceFileStore(t, path).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if len(records) != workers {
		t.Fatalf("records after concurrent distinct append = %d, want %d", len(records), workers)
	}
	gotClientOrderIDs := make(map[string]struct{}, len(records))
	for _, record := range records {
		if len(record.Orders) == 0 {
			t.Fatalf("record has no orders: %#v", record)
		}
		gotClientOrderIDs[record.Orders[0].ClientOrderID] = struct{}{}
	}
	if !reflect.DeepEqual(gotClientOrderIDs, wantClientOrderIDs) {
		t.Fatalf("retained client_order_ids = %#v, want %#v", gotClientOrderIDs, wantClientOrderIDs)
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

func TestPaperLifecycleEvidenceFileSinkReportsRollbackFailure(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "paper-evidence.jsonl"))
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	sink, err := newPaperLifecycleEvidenceFileSink(file)
	if err != nil {
		_ = file.Close()
		t.Fatalf("newPaperLifecycleEvidenceFileSink returned error: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	err = appendPaperLifecycleEvidenceJSONLRecord(sink, []byte("{}\n"))
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("appendPaperLifecycleEvidenceJSONLRecord error = %v, want rollback failure", err)
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

type paperEvidenceStoreAppendResult struct {
	committed bool
	err       error
}

func runConcurrentPaperEvidenceStoreAppends(t *testing.T, path string, sessions []*PaperLifecycleSession) []paperEvidenceStoreAppendResult {
	t.Helper()

	start := make(chan struct{})
	results := make([]paperEvidenceStoreAppendResult, len(sessions))
	var wg sync.WaitGroup
	for i, session := range sessions {
		wg.Add(1)
		go func(i int, session *PaperLifecycleSession) {
			defer wg.Done()
			store, err := NewPaperLifecycleEvidenceFileStore(path)
			if err != nil {
				results[i].err = err
				return
			}
			<-start
			results[i].committed, results[i].err = store.Append(session)
		}(i, session)
	}
	close(start)
	wg.Wait()
	return results
}

func completedTestPaperLifecycleSessionWithFirstClientOrderID(t *testing.T, clientOrderID string) *PaperLifecycleSession {
	t.Helper()

	session := completedTestPaperLifecycleSession(t)
	order := session.orders[1]
	order.ClientOrderID = clientOrderID
	session.orders[1] = order
	return session
}
