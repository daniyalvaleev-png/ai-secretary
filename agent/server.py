"""HTTP-обёртка над графом. Go-шлюз будет звать POST /draft."""

import logging

from dotenv import load_dotenv
from fastapi import FastAPI
from pydantic import BaseModel

load_dotenv()  # CALENDAR_URL и CALENDAR_API_KEY из agent/.env — до импорта графа

from graph import make_draft  # noqa: E402

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("agent")

app = FastAPI(title="AI Secretary Agent")


class DraftRequest(BaseModel):
    uid: int
    sender: str
    subject: str
    body: str


class MeetingOut(BaseModel):
    title: str
    start: str  # 2026-10-28 13:59
    end: str


class DraftResponse(BaseModel):
    uid: int
    draft: str
    attempts: int
    problems: str  # не пусто — даже после всех попыток черновик кривой
    meeting: MeetingOut | None = None  # для кнопки «В календарь»
    calendar_note: str = ""  # что агент узнал из календаря


@app.get("/health")
def health():
    return {"status": "ok"}


# Обычный def, не async: FastAPI сам вынесет долгий вызов Ollama в отдельный поток
@app.post("/draft", response_model=DraftResponse)
def draft(req: DraftRequest):
    log.info("✍️ Пишу черновик для UID %d", req.uid)

    result = make_draft(req.sender, req.subject, req.body)

    log.info("✅ UID %d: попыток %d, проблемы: %s", req.uid, result["attempts"], result["problems"] or "нет")
    if result["calendar_note"]:
        log.info("📅 UID %d: %s", req.uid, result["calendar_note"])

    return DraftResponse(
        uid=req.uid,
        draft=result["draft"],
        attempts=result["attempts"],
        problems=result["problems"],
        meeting=result["meeting"],
        calendar_note=result["calendar_note"],
    )