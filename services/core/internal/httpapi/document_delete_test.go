package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"tuskrag.local/core/internal/kb"
)

func TestDeleteDocumentRequiresCSRFAndReturnsAccepted(t *testing.T) {
	calls := 0
	documents := uploadTestDocuments{delete: func(_ context.Context, actor kb.Actor, kbID, documentID int64) error {
		if actor.TenantID != 1 || actor.UserID != 2 || kbID != 7 || documentID != 9 {
			t.Fatal("wrong document deletion target")
		}
		calls++
		return nil
	}}
	app := New(Dependencies{Identity: uploadTestIdentity{}, Documents: documents, InsecureLocalCookies: true})
	request := authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodDelete, "/api/v1/kbs/7/documents/9"
	request.Header.Del("X-CSRF-Token")
	response := httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("missing CSRF: status=%d calls=%d", response.Code, calls)
	}
	request = authenticatedUploadRequest(nil)
	request.Method, request.URL.Path = http.MethodDelete, "/api/v1/kbs/7/documents/9"
	response = httptest.NewRecorder()
	app.Handler.ServeHTTP(response, request)
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusAccepted || body.Status != "deleting" || calls != 1 {
		t.Fatalf("delete response: status=%d body=%s calls=%d err=%v", response.Code, response.Body.String(), calls, err)
	}
}
