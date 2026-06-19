package store

import (
	"testing"

	"ov-computeruse/server/internal/protocol"
)

func TestShouldStageHistoryItemsOnlyForMultiBatchUploads(t *testing.T) {
	if !shouldStageHistoryItems(protocol.HistoryItems{SessionID: "s1", UploadID: "u1", BatchCount: 2}) {
		t.Fatal("expected multi-batch upload to use staging")
	}
	for _, batch := range []protocol.HistoryItems{
		{SessionID: "s1", UploadID: "u1", BatchCount: 1},
		{SessionID: "s1", BatchCount: 2},
	} {
		if shouldStageHistoryItems(batch) {
			t.Fatalf("expected batch to bypass staging: %+v", batch)
		}
	}
}

func TestNormalizeHistoryItemsBatchValidatesUploadShape(t *testing.T) {
	valid, err := normalizeHistoryItemsBatch(protocol.HistoryItems{SessionID: " s1 ", UploadID: " u1 ", BatchIndex: 1, BatchCount: 2})
	if err != nil {
		t.Fatalf("normalize valid batch: %v", err)
	}
	if valid.SessionID != "s1" || valid.UploadID != "u1" {
		t.Fatalf("normalized ids = %q/%q, want s1/u1", valid.SessionID, valid.UploadID)
	}

	for _, batch := range []protocol.HistoryItems{
		{UploadID: "u1", BatchCount: 2},
		{SessionID: "s1", BatchCount: 2},
		{SessionID: "s1", UploadID: "u1"},
		{SessionID: "s1", UploadID: "u1", BatchIndex: 2, BatchCount: 2},
		{SessionID: "s1", UploadID: "u1", BatchIndex: -1, BatchCount: 2},
	} {
		if _, err := normalizeHistoryItemsBatch(batch); err == nil {
			t.Fatalf("expected invalid batch to be rejected: %+v", batch)
		}
	}
}
