package main

import (
	"encoding/json"
	"net/http"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func listUsers(w http.ResponseWriter, r *http.Request) {
	users := []User{{ID: 1, Name: "Alice"}, {ID: 2, Name: "Bob"}}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

func createUser(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusCreated)
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func main() {
	http.HandleFunc("/users", listUsers)
	http.HandleFunc("/health", healthCheck)
	http.ListenAndServe(":8080", nil)
}
