package argocd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/TheTangentLine/deployctl/internal/steps"
)

func TestGetOperation(t *testing.T) {
	type operationCase struct {
		name, request, state string
		want                 *steps.SyncOperationState
	}
	tests := []operationCase{
		{name: "no operation"},
		{
			name:    "no sync data",
			request: `{}`,
			state:   `{"phase":"Running","operation":{}}`,
		},
		{
			name:    "pending SHA",
			request: `{"sync":{"revision":"target-sha"}}`,
			want:    &steps.SyncOperationState{Revision: "target-sha", Phase: "Pending"},
		},
		{
			name:    "pending branch",
			request: `{"sync":{"revision":"main"}}`,
			want:    &steps.SyncOperationState{Revision: "main", Phase: "Pending"},
		},
		{
			name:    "pending empty revision",
			request: `{"sync":{}}`,
			want:    &steps.SyncOperationState{Phase: "Pending"},
		},
		{
			name:    "old completed operation at different revision",
			request: `{"sync":{"revision":"target-sha"}}`,
			state:   `{"phase":"Succeeded","message":"old success","operation":{"sync":{"revision":"old-sha"}},"syncResult":{"revision":"old-sha"}}`,
			want:    &steps.SyncOperationState{Revision: "target-sha", Phase: "Pending"},
		},
		{
			name:    "match requested revision rather than resolved revision",
			request: `{"sync":{"revision":"target-sha"}}`,
			state:   `{"phase":"Running","message":"old running sync","operation":{"sync":{"revision":"main"}},"syncResult":{"revision":"target-sha"}}`,
			want:    &steps.SyncOperationState{Revision: "target-sha", Phase: "Pending"},
		},
		{
			name:    "running state missing original sync cannot match",
			request: `{"sync":{"revision":"target-sha"}}`,
			state:   `{"phase":"Running","syncResult":{"revision":"target-sha"}}`,
			want:    &steps.SyncOperationState{Revision: "target-sha", Phase: "Pending"},
		},
		{
			name:    "matching empty requested revisions",
			request: `{"sync":{}}`,
			state:   `{"phase":"Running","message":"syncing default revision","operation":{"sync":{}}}`,
			want:    &steps.SyncOperationState{Phase: "Running", Message: "syncing default revision"},
		},
		{
			name:    "current request with missing recorded phase",
			request: `{"sync":{"revision":"target-sha"}}`,
			state:   `{"operation":{"sync":{"revision":"target-sha"}},"syncResult":{"revision":"target-sha"}}`,
			want:    &steps.SyncOperationState{Revision: "target-sha", Phase: "Pending"},
		},
		{
			name:  "recorded state without phase",
			state: `{"operation":{"sync":{"revision":"main"}},"syncResult":{"revision":"resolved-sha"}}`,
		},
		{
			name:  "fallback to unresolved branch",
			state: `{"phase":"Failed","message":"could not resolve revision","operation":{"sync":{"revision":"main"}}}`,
			want:  &steps.SyncOperationState{Revision: "main", Phase: "Failed", Message: "could not resolve revision"},
		},
		{
			name:  "empty resolved revision falls back to original revision",
			state: `{"phase":"Running","operation":{"sync":{"revision":"target-sha"}},"syncResult":{"revision":""}}`,
			want:  &steps.SyncOperationState{Revision: "target-sha", Phase: "Running"},
		},
		{
			name:  "empty revision does not use comparison revision",
			state: `{"phase":"Error","message":"missing revision","operation":{"sync":{}},"syncResult":{}}`,
			want:  &steps.SyncOperationState{Phase: "Error", Message: "missing revision"},
		},
		{
			name:  "sync result without original operation",
			state: `{"phase":"Succeeded","syncResult":{"revision":"resolved-sha"}}`,
			want:  &steps.SyncOperationState{Revision: "resolved-sha", Phase: "Succeeded"},
		},
		{
			name:  "sync result without any revision",
			state: `{"phase":"Error","message":"sync error","syncResult":{}}`,
			want:  &steps.SyncOperationState{Phase: "Error", Message: "sync error"},
		},
		{
			name:  "phase without sync data",
			state: `{"phase":"Succeeded","message":"unrelated operation"}`,
		},
		{
			name:  "unknown phase is preserved",
			state: `{"phase":"FuturePhase","operation":{"sync":{"revision":"target-sha"}}}`,
			want:  &steps.SyncOperationState{Revision: "target-sha", Phase: "FuturePhase"},
		},
		{
			name:    "null requested sync still permits latest recorded sync",
			request: `{"sync":null}`,
			state:   `{"phase":"Succeeded","operation":{"sync":{"revision":"target-sha"}}}`,
			want:    &steps.SyncOperationState{Revision: "target-sha", Phase: "Succeeded"},
		},
	}
	for _, phase := range []string{"Running", "Terminating"} {
		tests = append(tests, operationCase{
			name:    "matching active " + phase,
			request: `{"sync":{"revision":"main"}}`,
			state:   fmt.Sprintf(`{"phase":%q,"message":"active sync","operation":{"sync":{"revision":"main"}},"syncResult":{"revision":"resolved-sha"}}`, phase),
			want:    &steps.SyncOperationState{Revision: "resolved-sha", Phase: phase, Message: "active sync"},
		})
	}
	for _, phase := range []string{"Running", "Terminating", "Succeeded", "Failed", "Error"} {
		tests = append(tests, operationCase{
			name:  "latest recorded " + phase,
			state: fmt.Sprintf(`{"phase":%q,"message":"recorded sync","operation":{"sync":{"revision":"main"}},"syncResult":{"revision":"resolved-sha"}}`, phase),
			want:  &steps.SyncOperationState{Revision: "resolved-sha", Phase: phase, Message: "recorded sync"},
		})
	}
	for _, phase := range []string{"Succeeded", "Failed", "Error"} {
		tests = append(tests, operationCase{
			name:    "new request at same revision as completed " + phase,
			request: `{"sync":{"revision":"target-sha"}}`,
			state:   fmt.Sprintf(`{"phase":%q,"message":"old result","operation":{"sync":{"revision":"target-sha"}},"syncResult":{"revision":"target-sha"}}`, phase),
			want:    &steps.SyncOperationState{Revision: "target-sha", Phase: "Pending"},
		})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request, state := tc.request, tc.state
			if request == "" {
				request = "null"
			}
			if state == "" {
				state = "null"
			}
			body := fmt.Sprintf(`{"operation":%s,"status":{"sync":{"status":"Synced","revision":"comparison-sha"},"health":{"status":"Progressing"},"operationState":%s}}`, request, state)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, server.Client())
			got, err := client.Get(context.Background(), "backend-prod", false)
			want := steps.AppState{SyncStatus: "Synced", SyncRevision: "comparison-sha", Health: "Progressing", Operation: tc.want}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("state = %+v, operation = %+v, want %+v; error = %v", got, got.Operation, tc.want, err)
			}
		})
	}
}

func TestSyncOperationSnapshotsAreIndependent(t *testing.T) {
	var app application
	err := json.Unmarshal([]byte(`{"status":{"operationState":{"phase":"Running","message":"syncing","operation":{"sync":{"revision":"main"}},"syncResult":{"revision":"resolved-sha"}}}}`), &app)
	if err != nil {
		t.Fatal(err)
	}
	first := app.syncOperationState()
	if first == nil {
		t.Fatal("missing first snapshot")
	}
	want := *first
	*first = steps.SyncOperationState{Revision: "changed", Phase: "Failed", Message: "changed"}
	second := app.syncOperationState()
	if second == nil || second == first || *second != want {
		t.Fatalf("caller mutation leaked into snapshot: got %+v, want %+v", second, want)
	}
}
