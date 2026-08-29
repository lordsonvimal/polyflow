"""FastAPI service exposing a route-style WebSocket handler."""
from fastapi import FastAPI, WebSocket

app = FastAPI()


@app.websocket("/updates")
async def updates(websocket: WebSocket):
    await websocket.accept()
    while True:
        data = await websocket.receive_text()
        await websocket.send_text(data)
