"""Non-SSE StreamingResponse calls and other superficially similar shapes."""
from fastapi.responses import StreamingResponse


def download_file():
    return StreamingResponse(open("report.csv", "rb"), media_type="text/csv")


def other_call():
    return SomeOtherResponse(media_type="text/event-stream")
