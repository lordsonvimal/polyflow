"""Flask gateway: public-facing routes that proxy to the API service."""
from flask import Flask, request, jsonify
import requests as http

app = Flask(__name__)

API_BASE = "http://api:8000"


@app.route("/health")
def health_check():
    return jsonify({"status": "ok"})


@app.route("/webhook", methods=["POST"])
def process_webhook():
    payload = request.get_json()
    resp = http.post(f"{API_BASE}/notify/{payload['user_id']}", json=payload)
    return jsonify(resp.json()), resp.status_code


@app.route("/status")
def status_page():
    resp = http.get(f"{API_BASE}/users/")
    return jsonify({"user_count": len(resp.json())})
