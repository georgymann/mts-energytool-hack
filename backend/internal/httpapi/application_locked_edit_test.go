package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"energytool/backend/internal/service"
	"energytool/backend/internal/store"
)

func TestClientEditingDecidedApplicationGetsRecoverableConflict(t *testing.T) {
	for _, decision := range []string{"accept", "reject"} {
		t.Run(decision, func(t *testing.T) {
			db, err := store.NewFileStore(t.TempDir() + "/sessions.json")
			if err != nil {
				t.Fatal(err)
			}
			h := New(service.New(db, testML{}), nil, nil, time.Second).Handler()
			call := func(method, path, body string, status int) map[string]interface{} {
				t.Helper()
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
				if w.Code != status {
					t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
				}
				var out map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				return out
			}
			sid := call("POST", "/api/v1/sessions", "", 201)["id"].(string)
			path := "/api/v1/sessions/" + sid + "/application"
			body := `{"email":"audit@example.test","summary":"Раздельный учёт офиса"}`
			form := call("POST", path, body, 200)["application"].(map[string]interface{})
			prefix := "/api/v1/applications/" + form["id"].(string)
			call("POST", prefix+"/transitions", `{"expected_version":1,"action":"start_review"}`, 200)
			call("POST", prefix+"/transitions", fmt.Sprintf(`{"expected_version":2,"action":%q,"reason":"other"}`, decision), 200)
			error := call("POST", path, `{"email":"changed@example.test","summary":"Новое описание"}`, 409)
			if error["code"] != "application_closed" || !strings.Contains(error["message"].(string), "консультации") {
				t.Fatalf("unhelpful client error: %+v", error)
			}
			stored := call("GET", "/api/v1/sessions/"+sid, "", 200)["application"].(map[string]interface{})
			if stored["email"] != form["email"] || stored["summary"] != form["summary"] {
				t.Fatal("failed edit changed a decided application")
			}
			call("POST", "/api/v1/sessions/"+sid+"/messages", `{"content":"Дополнение к заявке: есть отдельный счётчик"}`, 200)
		})
	}
}
