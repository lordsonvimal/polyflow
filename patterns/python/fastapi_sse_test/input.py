"""FastAPI SSE endpoint via StreamingResponse."""
from fastapi import FastAPI
from fastapi.responses import StreamingResponse

app = FastAPI()


def event_generator():
    yield "data: hello\n\n"


@app.get("/stream")
async def stream_events():
    return StreamingResponse(event_generator(), media_type="text/event-stream")
