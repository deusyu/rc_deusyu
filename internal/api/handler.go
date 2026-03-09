package api

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/deusyu/rc_deusyu/internal/model"
	"github.com/deusyu/rc_deusyu/internal/store"
	"github.com/google/uuid"
)

type Handler struct {
	store *store.Store
}

func New(s *store.Store) *Handler {
	return &Handler{store: s}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/notifications", h.createNotification)
	mux.HandleFunc("GET /api/notifications/{id}", h.getNotification)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

type CreateRequest struct {
	TargetURL  string            `json:"target_url"`
	Method     string            `json:"method,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	MaxRetries *int              `json:"max_retries,omitempty"`
}

type CreateResponse struct {
	ID     string       `json:"id"`
	Status model.Status `json:"status"`
}

func (h *Handler) createNotification(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	if req.TargetURL == "" {
		http.Error(w, `{"error":"target_url is required"}`, http.StatusBadRequest)
		return
	}
	u, err := url.Parse(req.TargetURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		http.Error(w, `{"error":"target_url must be a valid http(s) URL"}`, http.StatusBadRequest)
		return
	}

	method := req.Method
	if method == "" {
		method = http.MethodPost
	}
	allowedMethods := map[string]bool{
		http.MethodGet: true, http.MethodPost: true,
		http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
	}
	if !allowedMethods[method] {
		http.Error(w, `{"error":"method must be one of GET, POST, PUT, PATCH, DELETE"}`, http.StatusBadRequest)
		return
	}

	maxRetries := 5
	if req.MaxRetries != nil {
		if *req.MaxRetries < 0 || *req.MaxRetries > 20 {
			http.Error(w, `{"error":"max_retries must be between 0 and 20"}`, http.StatusBadRequest)
			return
		}
		maxRetries = *req.MaxRetries
	}

	now := time.Now().UTC()
	n := &model.Notification{
		ID:         uuid.New().String(),
		TargetURL:  req.TargetURL,
		Method:     method,
		Headers:    req.Headers,
		Body:       req.Body,
		Status:     model.StatusPending,
		MaxRetries: maxRetries,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := h.store.Create(n); err != nil {
		log.Printf("ERROR create notification: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(CreateResponse{ID: n.ID, Status: n.Status})
}

func (h *Handler) getNotification(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := h.store.GetByID(id)
	if err != nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(n)
}
