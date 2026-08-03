package htx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPaperLifecycleEvidenceRecorderSuppressesRestartDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())
	session := completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-duplicate")
	input := testPaperLifecycleEvidenceRunInput("run-duplicate")

	committed, err := recorder.Record(session, input)
	if err != nil {
		t.Fatalf("Record initial returned error: %v", err)
	}
	if !committed {
		t.Fatalf("initial Record committed = false, want true")
	}
	before := mustReadFile(t, path)

	restarted := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())
	committed, err = restarted.Record(session, input)
	if err != nil {
		t.Fatalf("Record retry returned error: %v", err)
	}
	if committed {
		t.Fatalf("retry Record committed = true, want false for same run identity")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("file changed after duplicate retry:\nbefore=%s\nafter=%s", string(before), string(after))
	}

	readback, err := restarted.ReadCumulative()
	if err != nil {
		t.Fatalf("ReadCumulative returned error: %v", err)
	}
	if readback.RunCount != 1 || len(readback.Runs) != 1 {
		t.Fatalf("readback run count = %d/%d, want 1", readback.RunCount, len(readback.Runs))
	}
	if readback.Runs[0].RunInput != input {
		t.Fatalf("retained run input = %#v, want %#v", readback.Runs[0].RunInput, input)
	}
}

func TestPaperLifecycleEvidenceRecorderIgnoresExportedAtOnlyForRestartDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())
	session := completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-exported-at-retry")
	input := testPaperLifecycleEvidenceRunInput("run-exported-at-retry")

	firstExport := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	session.now = func() time.Time { return firstExport }
	committed, err := recorder.Record(session, input)
	if err != nil {
		t.Fatalf("Record initial returned error: %v", err)
	}
	if !committed {
		t.Fatalf("initial Record committed = false, want true")
	}
	before := mustReadFile(t, path)

	secondExport := time.Date(2026, 8, 3, 14, 5, 0, 0, time.UTC)
	session.now = func() time.Time { return secondExport }
	committed, err = recorder.Record(session, input)
	if err != nil {
		t.Fatalf("Record exported_at-only retry returned error: %v", err)
	}
	if committed {
		t.Fatalf("exported_at-only retry committed = true, want false")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("file changed after exported_at-only retry:\nbefore=%s\nafter=%s", string(before), string(after))
	}

	readback, err := recorder.ReadCumulative()
	if err != nil {
		t.Fatalf("ReadCumulative returned error: %v", err)
	}
	if readback.RunCount != 1 || len(readback.Runs) != 1 {
		t.Fatalf("readback run count = %d/%d, want 1", readback.RunCount, len(readback.Runs))
	}
	if readback.Runs[0].Evidence.ExportedAt != formatPaperEvidenceTime(firstExport) {
		t.Fatalf("retained exported_at = %s, want first committed export time", readback.Runs[0].Evidence.ExportedAt)
	}
}

func TestPaperLifecycleEvidenceRecorderRejectsDuplicateRunWithDifferentEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())
	input := testPaperLifecycleEvidenceRunInput("run-conflict")

	committed, err := recorder.Record(completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-run-conflict-a"), input)
	if err != nil {
		t.Fatalf("Record initial returned error: %v", err)
	}
	if !committed {
		t.Fatalf("initial Record committed = false, want true")
	}
	before := mustReadFile(t, path)

	committed, err = recorder.Record(completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-run-conflict-b"), input)
	if err == nil || !strings.Contains(err.Error(), "already exists with different evidence") {
		t.Fatalf("Record conflicting duplicate committed=%t err=%v, want conflicting duplicate failure", committed, err)
	}
	if committed {
		t.Fatalf("conflicting duplicate committed = true, want false")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("file changed after conflicting duplicate:\nbefore=%s\nafter=%s", string(before), string(after))
	}
}

func TestPaperLifecycleEvidenceRecorderAggregatesDistinctRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())

	for _, run := range []string{"run-a", "run-b"} {
		session := completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-"+run)
		committed, err := recorder.Record(session, testPaperLifecycleEvidenceRunInput(run))
		if err != nil {
			t.Fatalf("Record(%s) returned error: %v", run, err)
		}
		if !committed {
			t.Fatalf("Record(%s) committed = false, want true", run)
		}
	}

	readback, err := recorder.ReadCumulative()
	if err != nil {
		t.Fatalf("ReadCumulative returned error: %v", err)
	}
	assertTwoRunPaperLifecycleAggregate(t, readback)
}

func TestPaperLifecycleEvidenceRecorderReadbackIsOrderIndependent(t *testing.T) {
	dir := t.TempDir()
	source := testPaperLifecycleEvidenceSource()
	forward := mustPaperLifecycleEvidenceRecorder(t, filepath.Join(dir, "forward.jsonl"), source)
	reverse := mustPaperLifecycleEvidenceRecorder(t, filepath.Join(dir, "reverse.jsonl"), source)

	sessionA := completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-run-a")
	sessionB := completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-run-b")
	inputA := testPaperLifecycleEvidenceRunInput("run-a")
	inputB := testPaperLifecycleEvidenceRunInput("run-b")

	if committed, err := forward.Record(sessionA, inputA); err != nil || !committed {
		t.Fatalf("forward Record A committed=%t err=%v, want true", committed, err)
	}
	if committed, err := forward.Record(sessionB, inputB); err != nil || !committed {
		t.Fatalf("forward Record B committed=%t err=%v, want true", committed, err)
	}

	recordA := mustRecordedRun(t, reverse, sessionA, inputA)
	recordB := mustRecordedRun(t, reverse, sessionB, inputB)
	lineB := mustRecordedRunJSONL(t, recordB)
	lineA := mustRecordedRunJSONL(t, recordA)
	if err := os.WriteFile(reverse.Path(), append(lineB, lineA...), 0o600); err != nil {
		t.Fatalf("WriteFile(reverse) returned error: %v", err)
	}

	forwardReadback, err := forward.ReadCumulative()
	if err != nil {
		t.Fatalf("forward ReadCumulative returned error: %v", err)
	}
	reverseReadback, err := reverse.ReadCumulative()
	if err != nil {
		t.Fatalf("reverse ReadCumulative returned error: %v", err)
	}
	if !reflect.DeepEqual(forwardReadback.Summary, reverseReadback.Summary) {
		t.Fatalf("summaries differ:\nforward=%#v\nreverse=%#v", forwardReadback.Summary, reverseReadback.Summary)
	}
	if len(reverseReadback.Runs) != 2 || reverseReadback.Runs[0].RunID > reverseReadback.Runs[1].RunID {
		t.Fatalf("reverse runs are not sorted by run_id: %#v", reverseReadback.Runs)
	}
}

func TestPaperLifecycleEvidenceRecorderRejectsIncompatibleInputsAndRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())

	_, err := recorder.Record(completedTestPaperLifecycleSession(t), PaperLifecycleEvidenceRunInput{
		Source:  "other-source",
		Dataset: testPaperLifecycleEvidenceSource().Dataset,
		Run:     "run-a",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Record incompatible input error = %v, want source mismatch", err)
	}

	other := mustPaperLifecycleEvidenceRecorder(t, path, PaperLifecycleEvidenceSource{
		Mode:    string(ModeReplay),
		Source:  "other-source",
		Dataset: "fixture-v1",
	})
	record := mustRecordedRun(t, other, completedTestPaperLifecycleSession(t), PaperLifecycleEvidenceRunInput{
		Source:  "other-source",
		Dataset: "fixture-v1",
		Run:     "run-a",
	})
	if err := os.WriteFile(path, mustRecordedRunJSONL(t, record), 0o600); err != nil {
		t.Fatalf("WriteFile incompatible record returned error: %v", err)
	}
	_, err = recorder.ReadCumulative()
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("ReadCumulative incompatible record error = %v, want scope mismatch", err)
	}
}

func TestPaperLifecycleEvidenceRecorderFailsClosedOnStrictReadback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())
	record := mustRecordedRun(t, recorder, completedTestPaperLifecycleSession(t), testPaperLifecycleEvidenceRunInput("run-a"))
	line := mustRecordedRunJSONL(t, record)
	var asMap map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(line), &asMap); err != nil {
		t.Fatalf("Unmarshal canonical record returned error: %v", err)
	}
	asMap["unexpected"] = true
	corrupt, err := json.Marshal(asMap)
	if err != nil {
		t.Fatalf("Marshal corrupt record returned error: %v", err)
	}
	corrupt = append(corrupt, '\n')
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatalf("WriteFile corrupt record returned error: %v", err)
	}

	_, err = recorder.ReadCumulative()
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("ReadCumulative corrupt record error = %v, want unknown field failure", err)
	}
	before := mustReadFile(t, path)
	committed, err := recorder.Record(completedTestPaperLifecycleSession(t), testPaperLifecycleEvidenceRunInput("run-b"))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Record behind corrupt record committed=%t err=%v, want fail-closed read error", committed, err)
	}
	if committed {
		t.Fatalf("Record behind corrupt record committed = true, want false")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("file changed after append behind corrupt record:\nbefore=%s\nafter=%s", string(before), string(after))
	}
}

func TestPaperLifecycleEvidenceRecorderFailsClosedOnTruncatedEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())
	if committed, err := recorder.Record(completedTestPaperLifecycleSession(t), testPaperLifecycleEvidenceRunInput("run-a")); err != nil || !committed {
		t.Fatalf("Record committed=%t err=%v, want true", committed, err)
	}
	before := mustReadFile(t, path)
	if len(before) == 0 || before[len(before)-1] != '\n' {
		t.Fatalf("record file does not end in newline: %q", string(before))
	}
	if err := os.Truncate(path, int64(len(before)-1)); err != nil {
		t.Fatalf("Truncate returned error: %v", err)
	}
	truncated := mustReadFile(t, path)

	_, err := recorder.ReadCumulative()
	if err == nil || !strings.Contains(err.Error(), "record boundary") {
		t.Fatalf("ReadCumulative truncated record error = %v, want record-boundary failure", err)
	}
	committed, err := recorder.Record(completedTestPaperLifecycleSession(t), testPaperLifecycleEvidenceRunInput("run-b"))
	if err == nil || !strings.Contains(err.Error(), "record boundary") {
		t.Fatalf("Record behind truncated record committed=%t err=%v, want record-boundary failure", committed, err)
	}
	if committed {
		t.Fatalf("Record behind truncated record committed = true, want false")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, truncated) {
		t.Fatalf("file changed after append behind truncated record:\nbefore=%s\nafter=%s", string(truncated), string(after))
	}
}

func TestPaperLifecycleEvidenceRecorderPreservesCommittedRecordsOnPartialWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	recorder := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource())
	if committed, err := recorder.Record(completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-run-a"), testPaperLifecycleEvidenceRunInput("run-a")); err != nil || !committed {
		t.Fatalf("initial Record committed=%t err=%v, want true", committed, err)
	}
	before := mustReadFile(t, path)

	injected := errors.New("injected recorder partial write")
	recorder.newSink = func(file *os.File) (PaperLifecycleEvidenceJSONLSink, error) {
		sink, err := newPaperLifecycleEvidenceFileSink(file)
		if err != nil {
			return nil, err
		}
		return &partialPaperLifecycleEvidenceFileSink{sink: sink, limit: 37, err: injected}, nil
	}
	committed, err := recorder.Record(completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-run-b"), testPaperLifecycleEvidenceRunInput("run-b"))
	if !errors.Is(err, injected) {
		t.Fatalf("partial Record committed=%t err=%v, want injected write failure", committed, err)
	}
	if committed {
		t.Fatalf("partial Record committed = true, want false")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, before) {
		t.Fatalf("file changed after partial-write rollback:\nbefore=%s\nafter=%s", string(before), string(after))
	}
	readback, err := recorder.ReadCumulative()
	if err != nil {
		t.Fatalf("ReadCumulative returned error: %v", err)
	}
	if readback.RunCount != 1 {
		t.Fatalf("run count after partial write = %d, want 1", readback.RunCount)
	}
}

func TestPaperLifecycleEvidenceRecorderConcurrentDuplicateRunHasOneCommitter(t *testing.T) {
	const workers = 16
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	sessions := make([]*PaperLifecycleSession, workers)
	inputs := make([]PaperLifecycleEvidenceRunInput, workers)
	for i := range sessions {
		sessions[i] = completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-concurrent-duplicate")
		inputs[i] = testPaperLifecycleEvidenceRunInput("run-concurrent-duplicate")
	}

	results := runConcurrentPaperEvidenceRecorderRecords(t, path, testPaperLifecycleEvidenceSource(), sessions, inputs)
	committers := 0
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("worker %d Record returned error: %v", i, result.err)
		}
		if result.committed {
			committers++
		}
	}
	if committers != 1 {
		t.Fatalf("successful committers = %d, want 1", committers)
	}
	readback, err := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource()).ReadCumulative()
	if err != nil {
		t.Fatalf("ReadCumulative returned error: %v", err)
	}
	if readback.RunCount != 1 {
		t.Fatalf("run count after concurrent duplicate record = %d, want 1", readback.RunCount)
	}
}

func TestPaperLifecycleEvidenceRecorderConcurrentDistinctRunsRetainsEveryRecord(t *testing.T) {
	const workers = 16
	path := filepath.Join(t.TempDir(), "cumulative-evidence.jsonl")
	sessions := make([]*PaperLifecycleSession, workers)
	inputs := make([]PaperLifecycleEvidenceRunInput, workers)
	wantRuns := make(map[string]struct{}, workers)
	for i := range sessions {
		run := fmt.Sprintf("run-concurrent-%02d", i)
		sessions[i] = completedTestPaperLifecycleSessionWithFirstClientOrderID(t, "buy-open-"+run)
		inputs[i] = testPaperLifecycleEvidenceRunInput(run)
		wantRuns[run] = struct{}{}
	}

	results := runConcurrentPaperEvidenceRecorderRecords(t, path, testPaperLifecycleEvidenceSource(), sessions, inputs)
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("worker %d Record returned error: %v", i, result.err)
		}
		if !result.committed {
			t.Fatalf("worker %d Record committed = false, want true for distinct run", i)
		}
	}
	readback, err := mustPaperLifecycleEvidenceRecorder(t, path, testPaperLifecycleEvidenceSource()).ReadCumulative()
	if err != nil {
		t.Fatalf("ReadCumulative returned error: %v", err)
	}
	if readback.RunCount != workers {
		t.Fatalf("run count after concurrent distinct records = %d, want %d", readback.RunCount, workers)
	}
	gotRuns := make(map[string]struct{}, workers)
	for _, run := range readback.Runs {
		gotRuns[run.RunInput.Run] = struct{}{}
	}
	if !reflect.DeepEqual(gotRuns, wantRuns) {
		t.Fatalf("retained runs = %#v, want %#v", gotRuns, wantRuns)
	}
}

func mustPaperLifecycleEvidenceRecorder(t *testing.T, path string, source PaperLifecycleEvidenceSource) *PaperLifecycleEvidenceRecorder {
	t.Helper()

	recorder, err := NewPaperLifecycleEvidenceRecorder(path, source)
	if err != nil {
		t.Fatalf("NewPaperLifecycleEvidenceRecorder returned error: %v", err)
	}
	return recorder
}

func testPaperLifecycleEvidenceSource() PaperLifecycleEvidenceSource {
	return PaperLifecycleEvidenceSource{
		Mode:    string(ModeReplay),
		Source:  "local-replay-fixture",
		Dataset: "htx-btcusdt-2026-07-26-v1",
	}
}

func testPaperLifecycleEvidenceRunInput(run string) PaperLifecycleEvidenceRunInput {
	source := testPaperLifecycleEvidenceSource()
	return PaperLifecycleEvidenceRunInput{
		Source:  source.Source,
		Dataset: source.Dataset,
		Run:     run,
	}
}

func assertTwoRunPaperLifecycleAggregate(t *testing.T, readback PaperLifecycleCumulativeEvidence) {
	t.Helper()

	if readback.RunCount != 2 || len(readback.Runs) != 2 {
		t.Fatalf("run count = %d/%d, want 2", readback.RunCount, len(readback.Runs))
	}
	summary := readback.Summary
	if summary.OrderCount != 6 || summary.TradeCount != 4 || summary.LedgerEntryCount != 16 ||
		summary.ClosedTradeCount != 2 || summary.OpenTradeCount != 0 {
		t.Fatalf("aggregate counts = %#v, want exact two-run counts", summary)
	}
	if got := evidenceAmounts(summary.GrossTurnover)["USDT"]; got != "13562.213" {
		t.Fatalf("gross turnover USDT = %s, want 13562.213", got)
	}
	if got := evidenceAmounts(summary.GrossBuyTurnover)["USDT"]; got != "6782" {
		t.Fatalf("gross buy turnover USDT = %s, want 6782", got)
	}
	if got := evidenceAmounts(summary.GrossSellTurnover)["USDT"]; got != "6780.213" {
		t.Fatalf("gross sell turnover USDT = %s, want 6780.213", got)
	}
	totalFees := evidenceAmounts(summary.TotalFees)
	if totalFees["BTC"] != "0.0001" || totalFees["USDT"] != "6.780213" {
		t.Fatalf("total fees = %#v, want raw BTC/USDT fee totals", totalFees)
	}
	if got := evidenceAmounts(summary.EntryFees)["USDT"]; got != "6.782" {
		t.Fatalf("entry fees USDT = %s, want 6.782", got)
	}
	if got := evidenceAmounts(summary.ExitFees)["USDT"]; got != "6.780213" {
		t.Fatalf("exit fees USDT = %s, want 6.780213", got)
	}
	if got := evidenceAmounts(summary.GrossClosedPnL)["USDT"]; got != "4.995" {
		t.Fatalf("gross closed pnl USDT = %s, want 4.995", got)
	}
	if got := evidenceAmounts(summary.NetClosedPnL)["USDT"]; got != "-8.567213" {
		t.Fatalf("net closed pnl USDT = %s, want -8.567213", got)
	}
	if len(summary.ReconciliationDrift) != 0 {
		t.Fatalf("reconciliation drift = %#v, want none", summary.ReconciliationDrift)
	}
}

func mustRecordedRun(t *testing.T, recorder *PaperLifecycleEvidenceRecorder, session *PaperLifecycleSession, input PaperLifecycleEvidenceRunInput) PaperLifecycleRecordedRun {
	t.Helper()

	evidence, err := ExportPaperLifecycleEvidence(session)
	if err != nil {
		t.Fatalf("ExportPaperLifecycleEvidence returned error: %v", err)
	}
	record, err := recorder.recordForEvidence(evidence, input)
	if err != nil {
		t.Fatalf("recordForEvidence returned error: %v", err)
	}
	return record
}

func mustRecordedRunJSONL(t *testing.T, record PaperLifecycleRecordedRun) []byte {
	t.Helper()

	line, err := marshalPaperLifecycleRecordedRunJSONLRecord(record)
	if err != nil {
		t.Fatalf("marshalPaperLifecycleRecordedRunJSONLRecord returned error: %v", err)
	}
	return line
}

type partialPaperLifecycleEvidenceFileSink struct {
	sink  *paperLifecycleEvidenceFileSink
	limit int
	err   error
}

func (s *partialPaperLifecycleEvidenceFileSink) Len() int {
	return s.sink.Len()
}

func (s *partialPaperLifecycleEvidenceFileSink) Truncate(n int) error {
	return s.sink.Truncate(n)
}

func (s *partialPaperLifecycleEvidenceFileSink) Write(p []byte) (int, error) {
	n := s.limit
	if n > len(p) {
		n = len(p)
	}
	if n > 0 {
		if _, err := s.sink.Write(p[:n]); err != nil {
			return 0, err
		}
	}
	return n, s.err
}

type paperEvidenceRecorderRecordResult struct {
	committed bool
	err       error
}

func runConcurrentPaperEvidenceRecorderRecords(t *testing.T, path string, source PaperLifecycleEvidenceSource, sessions []*PaperLifecycleSession, inputs []PaperLifecycleEvidenceRunInput) []paperEvidenceRecorderRecordResult {
	t.Helper()

	start := make(chan struct{})
	results := make([]paperEvidenceRecorderRecordResult, len(sessions))
	var wg sync.WaitGroup
	for i, session := range sessions {
		wg.Add(1)
		go func(i int, session *PaperLifecycleSession) {
			defer wg.Done()
			recorder, err := NewPaperLifecycleEvidenceRecorder(path, source)
			if err != nil {
				results[i].err = err
				return
			}
			<-start
			results[i].committed, results[i].err = recorder.Record(session, inputs[i])
		}(i, session)
	}
	close(start)
	wg.Wait()
	return results
}
