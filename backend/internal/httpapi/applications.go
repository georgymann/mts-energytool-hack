package httpapi

import (
	"encoding/json"
	"energytool/backend/internal/business"
	"errors"
	"net/http"
	"time"
)

type applicationService interface {
	ListApplications(string) ([]business.ListItem, error)
	GetApplication(string) (business.ApplicationRecord, error)
	PilotMetrics(time.Time, time.Time, bool) (business.PilotMetrics, error)
	ChangeApplication(string, func(business.ApplicationRecord) (business.ApplicationRecord, []business.Event, error)) (business.ApplicationRecord, error)
}

func (s *Server) applicationsAPI(w http.ResponseWriter) (applicationService, bool) {
	api, ok := s.service.(applicationService)
	if !ok {
		writeJSON(w, 503, map[string]string{"code": "unavailable"})
		return nil, false
	}
	return api, true
}
func (s *Server) applicationError(w http.ResponseWriter, r *http.Request, err error) {
	cases := []struct {
		err    error
		status int
		code   string
	}{{business.ErrVersionConflict, 409, "version_conflict"}, {business.ErrApplicationClosed, 409, "application_closed"}, {business.ErrInvalidTransition, 409, "invalid_transition"}, {business.ErrRequestConflict, 409, "request_conflict"}, {business.ErrInvalidEdit, 400, "invalid_edit"}, {business.ErrReasonRequired, 400, "reason_required"}}
	for _, c := range cases {
		if errors.Is(err, c.err) {
			message := "Проверьте данные и актуальную версию заявки."
			if c.code == "application_closed" {
				message = "Менеджер уже обработал заявку: редактирование закрыто. Дополнения можно написать в консультации — они сохранятся в истории."
			}
			writeError(w, r, c.status, c.code, message)
			return
		}
	}
	s.mapError(w, r, err)
}
func (s *Server) listApplications(w http.ResponseWriter, r *http.Request) {
	api, ok := s.applicationsAPI(w)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && business.StatusLabel(business.ReviewStatus(status)) == "" {
		writeError(w, r, 400, "invalid_status", "Неизвестный статус.")
		return
	}
	items, err := api.ListApplications(status)
	if err != nil {
		s.applicationError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"applications": items})
}
func (s *Server) getApplication(w http.ResponseWriter, r *http.Request) {
	api, ok := s.applicationsAPI(w)
	if !ok {
		return
	}
	rec, err := api.GetApplication(r.PathValue("applicationID"))
	if err != nil {
		s.applicationError(w, r, err)
		return
	}
	writeJSON(w, 200, business.PublicView(rec))
}
func (s *Server) applicationReasons(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, business.ReasonCatalog())
}
func (s *Server) pilotMetrics(w http.ResponseWriter, r *http.Request) {
	api, ok := s.applicationsAPI(w)
	if !ok {
		return
	}
	var from, to time.Time
	for key, target := range map[string]*time.Time{"from": &from, "to": &to} {
		if raw := r.URL.Query().Get(key); raw != "" {
			v, e := time.ParseInLocation("2006-01-02", raw, business.Moscow)
			if e != nil {
				writeError(w, r, 400, "invalid_period", "Нужна дата ГГГГ-ММ-ДД.")
				return
			}
			*target = v
		}
	}
	if !from.IsZero() && !to.IsZero() && !to.After(from) {
		writeError(w, r, 400, "invalid_period", "Конец периода должен быть позже начала.")
		return
	}
	m, e := api.PilotMetrics(from, to, r.URL.Query().Get("include_demo") == "1")
	if e != nil {
		s.applicationError(w, r, e)
		return
	}
	writeJSON(w, 200, m)
}
func decodeCommand(w http.ResponseWriter, r *http.Request, value interface{}) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil || ensureEOF(d) != nil {
		writeError(w, r, 400, "invalid_json", "Некорректная команда.")
		return false
	}
	return true
}
func (s *Server) editApplication(w http.ResponseWriter, r *http.Request) {
	api, ok := s.applicationsAPI(w)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int             `json:"expected_version"`
		Actor           business.Actor  `json:"actor"`
		Edits           []business.Edit `json:"edits"`
		RequestID       string          `json:"client_request_id"`
	}
	if !decodeCommand(w, r, &body) {
		return
	}
	out, err := api.ChangeApplication(r.PathValue("applicationID"), func(rec business.ApplicationRecord) (business.ApplicationRecord, []business.Event, error) {
		n, e := business.EditCard(rec, body.ExpectedVersion, body.Actor, body.Edits, body.RequestID, time.Now())
		return n, nil, e
	})
	if err != nil {
		s.applicationError(w, r, err)
		return
	}
	writeJSON(w, 200, business.PublicView(out))
}
func (s *Server) transitionApplication(w http.ResponseWriter, r *http.Request) {
	api, ok := s.applicationsAPI(w)
	if !ok {
		return
	}
	var body struct {
		ExpectedVersion int                 `json:"expected_version"`
		Action          business.Action     `json:"action"`
		Reason          business.ReasonCode `json:"reason"`
		Comment         string              `json:"comment"`
		RequestID       string              `json:"client_request_id"`
	}
	if !decodeCommand(w, r, &body) {
		return
	}
	out, err := api.ChangeApplication(r.PathValue("applicationID"), func(rec business.ApplicationRecord) (business.ApplicationRecord, []business.Event, error) {
		return business.Transition(rec, body.Action, body.Reason, body.Comment, body.ExpectedVersion, body.RequestID, time.Now())
	})
	if err != nil {
		s.applicationError(w, r, err)
		return
	}
	writeJSON(w, 200, business.PublicView(out))
}
func (s *Server) exportApplication(w http.ResponseWriter, r *http.Request) {
	api, ok := s.applicationsAPI(w)
	if !ok {
		return
	}
	rec, err := api.GetApplication(r.PathValue("applicationID"))
	if err != nil {
		s.applicationError(w, r, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "json" {
		data, e := business.ExportJSON(rec.Card)
		if e != nil {
			s.applicationError(w, r, e)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=application.json")
		_, _ = w.Write(data)
		return
	}
	if format != "txt" && format != "" {
		writeError(w, r, 400, "invalid_format", "Поддерживаются txt и json.")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=application.txt")
	_, _ = w.Write([]byte(business.ExportTXT(rec.Card)))
}
