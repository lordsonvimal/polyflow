package main

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func main() {
	r := chi.NewRouter()
	r.Post("/users", CreateUser)
	r.Get("/users/{id}", GetUser)
	r.Get("/health", HealthCheck)
	http.ListenAndServe(":8080", r)
}

// CreateUser handles POST /users and calls the notification service.
func CreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	notifyUser(body.Email)
	w.WriteHeader(http.StatusCreated)
}

// notifyUser calls an external notification service.
func notifyUser(email string) {
	http.Post("http://notifications-svc/api/notify", "application/json", nil)
}

// GetUser handles GET /users/{id}.
func GetUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	json.NewEncoder(w).Encode(map[string]string{"id": id})
}

// HealthCheck handles GET /health.
func HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
