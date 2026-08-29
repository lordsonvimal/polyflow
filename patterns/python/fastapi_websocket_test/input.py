"""FastAPI WebSocket message-level pumps."""
from fastapi import FastAPI, WebSocket

app = FastAPI()


@app.websocket("/ws/notifications")
async def notifications_socket(websocket: WebSocket):
    await websocket.accept()
    message = await websocket.receive_text()
    await websocket.send_json({"type": "ack", "echo": message})
