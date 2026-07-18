"""Flask application with both @app.route and Flask 2.0 shorthand decorators."""
from flask import Flask

app = Flask(__name__)


@app.route("/users")
def list_users():
    return []


@app.route("/users/<int:user_id>", methods=["DELETE"])
def delete_user(user_id):
    return {}, 204


@app.get("/health")
def health_check():
    return "ok"


@app.post("/users")
def create_user():
    return {}, 201
