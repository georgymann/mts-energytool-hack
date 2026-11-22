package httpapi

import (
	"encoding/json"
	"energytool/backend/internal/service"
	"energytool/backend/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplicationLiveLifecycle(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	db, e := store.NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	app := service.New(db, testML{})
	h := New(app, nil, nil, time.Second).Handler()
	call := func(method, path, body string, status int) map[string]interface{} {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	sid := call("POST", "/api/v1/sessions", "", 201)["id"].(string)
	call("POST", "/api/v1/sessions/"+sid+"/messages", `{"content":"Счётчики на складе, 120 кВт"}`, 200)
	saved := call("POST", "/api/v1/sessions/"+sid+"/application", `{"email":"jury@example.test","summary":"Задача клиента:\n• Учёт на складе\n\nОбъект:\n• Склад","client_request_id":"save1"}`, 200)
	id := saved["application"].(map[string]interface{})["id"].(string)
	prefix := "/api/v1/applications/" + id
	queue := call("GET", "/api/v1/applications", "", 200)
	if len(queue["applications"].([]interface{})) != 1 {
		t.Fatal(queue)
	}
	card := call("GET", prefix, "", 200)["card"].(map[string]interface{})
	if card["task"].(map[string]interface{})["value"] != "Учёт на складе" {
		t.Fatal(card)
	}
	call("GET", "/api/v1/applications/reasons", "", 200)
	call("PATCH", prefix+"/card", `{"expected_version":1,"actor":"manager","edits":[{"path":"task","action":"set","value":"Уточнить зоны склада"}],"client_request_id":"edit1"}`, 200)
	start := `{"expected_version":2,"action":"start_review","client_request_id":"review1"}`
	call("POST", prefix+"/transitions", start, 200)
	call("POST", prefix+"/transitions", start, 200)
	call("POST", prefix+"/transitions", `{"expected_version":2,"action":"reject","reason":"other","client_request_id":"conflict"}`, 409)
	accepted := call("POST", prefix+"/transitions", `{"expected_version":3,"action":"accept","client_request_id":"accept1"}`, 200)
	if accepted["version"] != float64(4) {
		t.Fatal(accepted)
	}
	m := call("GET", "/api/v1/metrics/pilot", "", 200)
	if m["sessions_with_application"] != float64(1) || m["status_counts"].(map[string]interface{})["accepted"] != float64(1) {
		t.Fatal(m)
	}
	call("GET", prefix+"/export?format=json", "", 200)
	call("GET", prefix+"/export?format=txt", "", 200)
	db, e = store.NewFileStore(path)
	if e != nil {
		t.Fatal(e)
	}
	h = New(service.New(db, testML{}), nil, nil, time.Second).Handler()
	restored := call("GET", prefix, "", 200)
	if restored["status"] != "accepted" {
		t.Fatal(restored)
	}
	session, e := db.Get(sid)
	if e != nil || session.Application.Summary != "Уточнить зоны склада" {
		t.Fatal(e, session.Application)
	}
	// HTTP result fields never contain internal command fingerprints.
	if _, ok := restored["request_hashes"]; ok {
		t.Fatal("private hashes exposed")
	}
}
