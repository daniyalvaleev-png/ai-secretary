"""LangGraph-граф: пишет черновик ответа на письмо и сам проверяет его.

START -> write_draft -> check_draft -> есть проблемы и попытки не кончились?
                                          да  -> снова write_draft
                                          нет -> END
"""

import os
from typing import TypedDict

from langchain_core.messages import AIMessage, HumanMessage, SystemMessage
from langchain_ollama import ChatOllama
from langgraph.graph import END, START, StateGraph


MAX_ATTEMPTS = 3

# Берём базовую granite, а не secretary: secretary заточена отвечать только JSON
llm = ChatOllama(
    model=os.getenv("DRAFT_MODEL", "granite4.2:3b"),
    base_url=os.getenv("OLLAMA_URL", "http://localhost:11434"),
    temperature=0.7,
    keep_alive="30m",
        reasoning=False,  # granite4.2 думает перед ответом — для коротких черновиков это лишние минуты
)

SYSTEM_PROMPT = """Ты пишешь ответ на письмо от имени владельца почты — от первого лица, как будто он сам отвечает.

Правила:
- Только русский язык, простые живые фразы, как пишет обычный человек.
- Коротко: 1–3 предложения.
- Тон как у отправителя: если он на «ты» и пишет неформально — отвечай на «ты» и просто; если на «вы» — вежливо на «вы».
- Используй только факты из письма. Не добавляй своих условий, времени, мест и обещаний.
- Если чего-то не хватает для ответа — коротко уточни.
- Не цитируй грубости из письма.
- Без темы, без подписи, без заготовок вроде [Имя].
- Никогда не соглашайся от имени владельца на сделки, деньги и обязательства — ответь, что вернёшься с ответом позже.

Выведи только текст ответа."""

# Few-shot: пара примеров «письмо → хороший ответ». Маленькие модели копируют стиль примеров лучше, чем следуют правилам
EXAMPLES = [
    HumanMessage("От: ivan@mail.ru\nТема: созвон\n\nздарова, го созвонимся завтра в 18?"),
    AIMessage("Привет! Давай, завтра в 18 норм. Позвоню сам."),
    HumanMessage("От: hr@company.ru\nТема: Собеседование\n\nЗдравствуйте! Приглашаем вас на собеседование в четверг. Удобно ли вам?"),
    AIMessage("Здравствуйте! Спасибо за приглашение. В четверг удобно — подскажите, пожалуйста, время и формат встречи."),
]


class DraftState(TypedDict):
    sender: str
    subject: str
    body: str
    draft: str
    problems: str  # что не так с последним черновиком; пусто — всё ок
    attempts: int


def write_draft(state: DraftState) -> dict:
    text = f"От: {state['sender']}\nТема: {state['subject']}\n\n{state['body']}"

    # На повторной попытке говорим модели, где она накосячила
    if state["problems"]:
        text += f"\n\nПрошлый черновик не подошёл: {state['problems']}. Напиши заново."

        resp = llm.invoke([SystemMessage(SYSTEM_PROMPT), *EXAMPLES, HumanMessage(text)])
        return {"draft": resp.content.strip(), "attempts": state["attempts"] + 1}


def check_draft(state: DraftState) -> dict:
    draft = state["draft"]
    problems = []

    if len(draft) < 10:
        problems.append("ответ пустой или слишком короткий")
    if len(draft) > 500:
        problems.append("ответ слишком длинный")

    letters = [c for c in draft.lower() if c.isalpha()]
    cyrillic = [c for c in letters if "а" <= c <= "я" or c == "ё"]
    if letters and len(cyrillic) / len(letters) < 0.7:
        problems.append("ответ не на русском языке")

    if "[" in draft and "]" in draft:
        problems.append("в ответе остались заготовки в квадратных скобках")

    return {"problems": "; ".join(problems)}


def route_after_check(state: DraftState) -> str:
    if state["problems"] and state["attempts"] < MAX_ATTEMPTS:
        return "write_draft"
    return END


builder = StateGraph(DraftState)
builder.add_node("write_draft", write_draft)
builder.add_node("check_draft", check_draft)
builder.add_edge(START, "write_draft")
builder.add_edge("write_draft", "check_draft")
builder.add_conditional_edges("check_draft", route_after_check, ["write_draft", END])

draft_graph = builder.compile()


def make_draft(sender: str, subject: str, body: str) -> DraftState:
    return draft_graph.invoke(
        {
            "sender": sender,
            "subject": subject,
            "body": body,
            "draft": "",
            "problems": "",
            "attempts": 0,
        }
    )