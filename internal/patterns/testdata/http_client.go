package main

import "net/http"

func fetchData(url string) {
	http.Get(url)
	http.Post(url, "application/json", nil)
	req, _ := http.NewRequest("PUT", url, nil)
	_ = req
}
