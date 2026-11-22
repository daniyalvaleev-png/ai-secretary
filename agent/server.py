"""HTTP-обёртка над графом. Go-шлюз зовёт POST /draft.

Агента зовёт только шлюз — и пропускает его по общему секрету AGENT_API_KEY (тот же ключ в go-gateway/.env).
Без ключа любая программа на компе (или в сети, если агент когда-нибудь будет слушать не только 127.0.0.1)
могла бы гонять модель и читать черновики.
"""

import logging
import os
import secrets

from dotenv import load_dotenv
from fastapi import Depends, FastAPI, Header, HTTPException
from pydantic import BaseModel, Field

load_dotenv()  # CALENDAR_URL, CALENDAR_API_KEY и AGENT_API_KEY из agent/.env — до импорта графа

AGENT_API_KEY = os.getenv("AGENT_API_KEY", "").strip()
if not AGENT_API_KEY:
    raise SystemExit("❌ Задай AGENT_API_KEY в agent/.env (тот же, что в go-gateway/.env) — без ключа агента не запускаю")

from graph import make_draft  # noqa: E402

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
log = logging.getLogger("agent")

# Документация /docs и /openapi.json не нужны — это внутренний сервис, лишнее не светим
app = FastAPI(title="AI Secretary Agent", docs_url=None, redoc_url=None, openapi_url=None)


def require_key(authorization: str = Header(default="")) -> None:
    """Пускает только запросы с заголовком Authorization: Bearer <AGENT_API_KEY>"""
    given = authorization.removeprefix("Bearer ").strip()
    # compare_digest сравнивает за одинаковое время — по скорости ответа ключ не подобрать
    if not secrets.compare_digest(given.encode(), AGENT_API_KEY.encode()):
        raise HTTPException(status_code=401, detail="нужен заголовок Authorization: Bearer <AGENT_API_KEY>")


class DraftRequest(BaseModel):
    # Ограничения длины — шлюз и так режет письмо до 4000 символов, это на случай чужого клиента
    uid: int
    sender: str = Field(max_length=500)
    subject: str = Field(max_length=1000)
    body: str = Field(max_length=20000)


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
@app.post("/draft", response_model=DraftResponse, dependencies=[Depends(require_key)])
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