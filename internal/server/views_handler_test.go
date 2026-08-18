package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/ops"
)

func TestViews_CreateListRoundTrip(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	body := bytes.NewBufferString(`{"name":"fleet rabbitmq seam","state":"eyJ2IjoxfQ"}`)
	req := httptest.NewRequest("POST", "/api/views", body)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body)
	}
	var created struct {
		View ops.View `json:"view"`
	}
	decodeJSON(t, w.Body.Bytes(), &created)
	if created.View.Name != "fleet rabbitmq seam" || created.View.ID == 0 {
		t.Fatalf("unexpected created view: %+v", created.View)
	}

	req2 := httptest.NewRequest("GET", "/api/views", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w2.Code, w2.Body)
	}
	var listed struct {
		Views []ops.View `json:"views"`
	}
	decodeJSON(t, w2.Body.Bytes(), &listed)
	if len(listed.Views) != 1 || listed.Views[0].Name != "fleet rabbitmq seam" {
		t.Fatalf("unexpected list: %+v", listed.Views)
	}
}

func TestViews_CreateDuplicateNameConflicts(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	mk := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/views", bytes.NewBufferString(`{"name":"dup","state":"s"}`))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	if w := mk(); w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body)
	}
	if w := mk(); w.Code != http.StatusConflict {
		t.Fatalf("want 409 on duplicate name, got %d: %s", w.Code, w.Body)
	}
}

func TestViews_DeleteAndNotFound(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	req := httptest.NewRequest("POST", "/api/views", bytes.NewBufferString(`{"name":"to-delete","state":"s"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var created struct {
		View ops.View `json:"view"`
	}
	decodeJSON(t, w.Body.Bytes(), &created)

	del := httptest.NewRequest("DELETE", "/api/views/"+strconv.FormatInt(created.View.ID, 10), nil)
	wDel := httptest.NewRecorder()
	srv.ServeHTTP(wDel, del)
	if wDel.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", wDel.Code, wDel.Body)
	}

	del2 := httptest.NewRequest("DELETE", "/api/views/"+strconv.FormatInt(created.View.ID, 10), nil)
	wDel2 := httptest.NewRecorder()
	srv.ServeHTTP(wDel2, del2)
	if wDel2.Code != http.StatusNotFound {
		t.Fatalf("want 404 on repeat delete, got %d: %s", wDel2.Code, wDel2.Body)
	}
}

func TestViews_Rename(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	req := httptest.NewRequest("POST", "/api/views", bytes.NewBufferString(`{"name":"old-name","state":"s"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var created struct {
		View ops.View `json:"view"`
	}
	decodeJSON(t, w.Body.Bytes(), &created)

	ren := httptest.NewRequest("PATCH", "/api/views/"+strconv.FormatInt(created.View.ID, 10), bytes.NewBufferString(`{"name":"new-name"}`))
	wRen := httptest.NewRecorder()
	srv.ServeHTTP(wRen, ren)
	if wRen.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", wRen.Code, wRen.Body)
	}
	var renamed struct {
		View ops.View `json:"view"`
	}
	decodeJSON(t, wRen.Body.Bytes(), &renamed)
	if renamed.View.Name != "new-name" {
		t.Fatalf("want renamed view, got %+v", renamed.View)
	}
}

func TestViews_WithoutOpsIsUnavailable(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())

	req := httptest.NewRequest("GET", "/api/views", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body)
	}
}
