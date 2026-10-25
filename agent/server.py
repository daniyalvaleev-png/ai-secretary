"""HTTP-обёртка над графом. Go-шлюз будет звать POST /draft."""

import logging

from fastapi import FastAPI
from pydantic import BaseModel

from graph import make_draft

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("agent")

app = FastAPI(title="AI Secretary Agent")


class DraftRequest(BaseModel):
    uid: int
    sender: str
    subject: str
    body: str


class DraftResponse(BaseModel):
    uid: int
    draft: str
    attempts: int
    problems: str  # не пусто — даже после всех попыток черновик кривой


@app.get("/health")
def health():
    return {"status": "ok"}


# Обычный def, не async: FastAPI сам вынесет долгий вызов Ollama в отдельный поток
@app.post("/draft", response_model=DraftResponse)
def draft(req: DraftRequest):
    log.info("✍️ Пишу черновик для UID %d", req.uid)

    result = make_draft(req.sender, req.subject, req.body)

    log.info("✅ UID %d: попыток %d, проблемы: %s", req.uid, result["attempts"], result["problems"] or "нет")

    return DraftResponse(
        uid=req.uid,
        draft=result["draft"],
        attempts=result["attempts"],
        problems=result["problems"],
    )