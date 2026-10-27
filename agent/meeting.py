"""Встречи в письмах: модель только достаёт дату и время, а календарь проверяет код.

Маленькая модель путает «завтра» и «послезавтра» и плохо сама водит инструменты,
поэтому даты ей подсказываем готовой таблицей, а MCP-календарь зовём из кода.
"""

import asyncio
import json
import logging
import os
import re
from datetime import date, datetime, timedelta
from typing import TypedDict

from langchain_core.messages import HumanMessage, SystemMessage
from langchain_mcp_adapters.client import MultiServerMCPClient
from langchain_ollama import ChatOllama

log = logging.getLogger("agent")

WEEKDAYS = ["понедельник", "вторник", "среда", "четверг", "пятница", "суббота", "воскресенье"]
WEEKDAYS_ACC = ["в понедельник", "во вторник", "в среду", "в четверг", "в пятницу", "в субботу", "в воскресенье"]
DAYS_AHEAD = 14  # насколько вперёд модель может назначать встречи
WORK_START, WORK_END = 9, 21  # в какие часы владелец вообще готов встречаться

MEETING_SCHEMA = {
    "type": "object",
    "properties": {
        "есть_встреча": {"type": "boolean"},
        "дата": {"type": "string"},
        "время": {"type": "string"},
        "длительность_минут": {"type": "integer"},
        "название": {"type": "string"},
    },
    "required": ["есть_встреча", "дата", "время", "длительность_минут", "название"],
}

extractor = ChatOllama(
    model=os.getenv("DRAFT_MODEL", "granite4.2:3b"),
    base_url=os.getenv("OLLAMA_URL", "http://localhost:11434"),
    temperature=0,
    keep_alive="30m",
    reasoning=False,
    format=MEETING_SCHEMA,  # structured output, как в Go-шлюзе
)


class Extracted(TypedDict):
    date: str  # 2026-10-28
    time: str  # 13:59 или "" — время не назвали
    duration: int
    title: str


class Meeting(TypedDict):
    title: str
    start: str  # 2026-10-28 13:59
    end: str


def date_table(today: date) -> str:
    """«сегодня — 2026-10-26, понедельник» и т.д. Модель выбирает дату из таблицы, а не считает сама."""
    labels = {0: "сегодня", 1: "завтра", 2: "послезавтра"}
    lines = []
    for i in range(DAYS_AHEAD):
        d = today + timedelta(days=i)
        prefix = labels.get(i, "")
        lines.append(f"{prefix + ' — ' if prefix else ''}{d.isoformat()}, {WEEKDAYS[d.weekday()]}")
    return "\n".join(lines)


def extract_meeting(sender: str, subject: str, body: str, today: date) -> Extracted | None:
    system = f"""Ты достаёшь из письма предложение встретиться, созвониться или прийти на собеседование.
- "есть_встреча": true, только если отправитель предлагает встречу или звонок в конкретный день.
- "дата": дата из таблицы ниже, ГГГГ-ММ-ДД. Ничего не считай сам — найди нужный день в таблице. Если день не назван — пустая строка.
- "время": ЧЧ:ММ, если время названо («в 13 часов 59 минут» = 13:59, «в 6 вечера» = 18:00). Иначе пустая строка.
- "длительность_минут": если сказано, иначе 60.
- "название": 2–5 слов, например «Встреча с Васей в парке».

Таблица дат:
{date_table(today)}"""

    resp = extractor.invoke([SystemMessage(system), HumanMessage(f"От: {sender}\nТема: {subject}\n\n{body}")])
    try:
        raw = json.loads(resp.content)
    except json.JSONDecodeError:
        log.warning("⚠️ Модель вернула кривой JSON встречи: %r", resp.content)
        return None

    return validate(raw, today, f"{subject} {body}")


# «в пятницу», «во вторник», «в среду» — корни, чтобы ловить все падежи
WEEKDAY_ROOTS = ["понедельник", "вторник", "сред", "четверг", "пятниц", "суббот", "воскресен"]


# «3 ноября», «15 января» — месяцы в родительном падеже
MONTHS_GEN = ["января", "февраля", "марта", "апреля", "мая", "июня",
              "июля", "августа", "сентября", "октября", "ноября", "декабря"]


def nearest_date(day: int, month: int, today: date) -> date | None:
    """Ближайшая такая дата не раньше сегодня: «3 ноября» в декабре — это уже следующий год."""
    for year in (today.year, today.year + 1):
        try:
            d = date(year, month, day)
        except ValueError:  # «31 ноября»
            return None
        if d >= today:
            return d
    return None


def explicit_date(low: str, today: date) -> date | None:
    """«3 ноября», «03.11», «3.11.2026». Тут модель путалась: «3 ноября» записала на 31 октября."""
    m = re.search(r"\b(\d{1,2})\s+(" + "|".join(MONTHS_GEN) + r")\b", low)
    if m:
        return nearest_date(int(m[1]), MONTHS_GEN.index(m[2]) + 1, today)

    # «03.11» или «3.11.2026». Время «10.30» сюда не попадёт: у даты месяц от 1 до 12
    m = re.search(r"\b(\d{1,2})\.(\d{1,2})(?:\.(\d{4}))?\b", low)
    if m and 1 <= int(m[2]) <= 12:
        if m[3]:
            try:
                return date(int(m[3]), int(m[2]), int(m[1]))
            except ValueError:
                return None
        return nearest_date(int(m[1]), int(m[2]), today)
    return None


def date_from_words(text: str, today: date) -> date | None:
    """Даты считает код, а не модель — она ошибалась (пятницу записала на четверг, 3 ноября — на 31 октября)."""
    low = text.lower()
    if d := explicit_date(low, today):  # явная дата важнее «завтра»
        return d
    if "следующ" in low:  # «в следующую пятницу» — неоднозначно, пусть решает модель
        return None
    if "послезавтра" in low:  # проверяем раньше «завтра» — оно сидит внутри «послезавтра»
        return today + timedelta(days=2)
    if "завтра" in low:
        return today + timedelta(days=1)
    if "сегодня" in low:
        return today
    for weekday, root in enumerate(WEEKDAY_ROOTS):
        if re.search(r"\b(в|во)\s+" + root, low):
            return today + timedelta(days=(weekday - today.weekday()) % 7 or 7)
    return None


def time_in_text(hour: int, minute: int, text: str) -> bool:
    """Есть ли это время в письме: «18:00», «в 18», «в 6 вечера». Модель любит выдумывать время из примеров."""
    numbers = {int(n) for n in re.findall(r"\d+", text)}
    hour_mentioned = hour in numbers or (hour % 12 or 12) in numbers
    return hour_mentioned and (minute == 0 or minute in numbers)


def validate(raw: dict, today: date, text: str) -> Extracted | None:
    """Не верим модели на слово: дата должна быть из таблицы, время — настоящим и взятым из письма."""
    if not raw.get("есть_встреча"):
        return None

    # «завтра», «в пятницу» код понимает сам и надёжнее модели; модель нужна для «3 ноября»
    d = date_from_words(text, today)
    if d is None:
        try:
            d = date.fromisoformat(raw.get("дата", ""))
        except ValueError:
            return None
    if not today <= d < today + timedelta(days=DAYS_AHEAD):
        return None

    t = ""
    m = re.fullmatch(r"(\d{1,2}):(\d{2})", raw.get("время", "").strip())
    if m and int(m[1]) < 24 and int(m[2]) < 60 and time_in_text(int(m[1]), int(m[2]), text):
        t = f"{int(m[1]):02d}:{m[2]}"

    duration = raw.get("длительность_минут") or 60
    if not 5 <= duration <= 8 * 60:
        duration = 60

    title = raw.get("название", "").strip() or "Встреча"
    return Extracted(date=d.isoformat(), time=t, duration=duration, title=title)


# --- Календарь по MCP ---

def calendar_enabled() -> bool:
    return bool(os.getenv("CALENDAR_API_KEY"))


def call_calendar(tool: str, args: dict) -> dict:
    """Синхронная обёртка: граф у нас синхронный, а MCP-клиент асинхронный."""

    async def run():
        client = MultiServerMCPClient({
            "calendar": {
                "transport": "streamable_http",
                "url": os.getenv("CALENDAR_URL", "http://127.0.0.1:8095/mcp"),
                "headers": {"Authorization": "Bearer " + os.environ["CALENDAR_API_KEY"]},
            }
        })
        tools = {t.name: t for t in await client.get_tools()}
        result = await tools[tool].ainvoke(args)
        return json.loads(result[0]["text"])

    return asyncio.run(run())


def suggest_times(slots: list[dict], wanted: datetime | None, duration: int, limit: int = 2) -> list[str]:
    """Конкретные времена вместо диапазонов: ближайшие к запрошенному, которые влезают в свободные окна.
    Окно до встречи → самое позднее начало в нём, окно после → самое раннее."""
    candidates = []
    for s in slots:
        start = datetime.fromisoformat(s["start"])
        end = datetime.fromisoformat(s["end"])
        latest = end - timedelta(minutes=duration)
        pick = latest if wanted and latest <= wanted else start
        candidates.append(pick)

    if wanted:
        candidates.sort(key=lambda c: abs(c - wanted))
    return sorted(c.strftime("%H:%M") for c in candidates[:limit])


def check_calendar(ex: Extracted) -> tuple[str, Meeting | None]:
    """Возвращает (факты для черновика, встречу для кнопки «В календарь»).

    Факты — сухие, без указаний («ЗАНЯТО. Можно предложить: …»): иначе модель пересказывает их в ответе.
    Что с ними делать, модель видит в примерах SYSTEM_PROMPT."""
    day = WEEKDAYS_ACC[date.fromisoformat(ex["date"]).weekday()]

    meeting = None
    if ex["time"]:
        start = datetime.fromisoformat(f"{ex['date']} {ex['time']}")
        end = start + timedelta(minutes=ex["duration"])
        meeting = Meeting(title=ex["title"], start=start.strftime("%Y-%m-%d %H:%M"), end=end.strftime("%Y-%m-%d %H:%M"))

    if not calendar_enabled():
        return "", meeting

    try:
        free = call_calendar("find_free_slots", {
            "date": ex["date"],
            "duration_minutes": ex["duration"],
            "work_start_hour": WORK_START,
            "work_end_hour": WORK_END,
        })
    except Exception as e:  # календарь лежит — черновик всё равно нужен, просто без фактов
        log.warning("⚠️ Календарь недоступен: %s", e)
        return "", meeting

    slots = free["slots"]
    whole_day = len(slots) == 1 and slots[0]["start"][11:] == f"{WORK_START:02d}:00" and slots[0]["end"][11:] == f"{WORK_END:02d}:00"

    if meeting is None:  # день назвали, а время нет
        if whole_day:
            return f"Календарь: {day} — свободен весь день.", None
        if not slots:
            return f"Календарь: {day} — всё занято.", None
        return f"Календарь: {day} — можно предложить: {' или '.join(suggest_times(slots, None, ex['duration']))}.", None

    # Строки формата «2026-10-28 13:59» сравниваются как даты — можно обойтись без парсинга
    if any(s["start"] <= meeting["start"] and meeting["end"] <= s["end"] for s in slots):
        return f"Календарь: {day} в {ex['time']} — СВОБОДНО.", meeting

        # Время занято — встреча ещё не согласована: мы предлагаем другое время, поэтому кнопки «В календарь» нет
    wanted = datetime.fromisoformat(meeting["start"])
    options = suggest_times(slots, wanted, ex["duration"])
    if not options:
        return f"Календарь: {day} в {ex['time']} — ЗАНЯТО, и весь день занят.", None
    return f"Календарь: {day} в {ex['time']} — ЗАНЯТО. Можно предложить: {' или '.join(options)}.", None