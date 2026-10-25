"""LangGraph-граф: пишет черновик ответа на письмо и сам проверяет его.

START -> write_draft -> check_draft -> есть проблемы и попытки не кончились?
                                          да  -> снова write_draft
                                          нет -> END
"""

import os
from typing import TypedDict

from langchain_core.messages import HumanMessage, SystemMessage
from langchain_ollama import ChatOllama
from langgraph.graph import END, START, StateGraph

MAX_ATTEMPTS = 3

# Берём базовую granite, а не secretary: secretary заточена отвечать только JSON
llm = ChatOllama(
    model=os.getenv("DRAFT_MODEL", "granite4.2:3b"),
    base_url=os.getenv("OLLAMA_URL", "http://localhost:11434"),
    temperature=0.3,
)

SYSTEM_PROMPT = """Ты — ИИ-секретарь. Пишешь черновик ответа на входящее письмо от имени владельца почты.

Правила:
- Пиши только на русском языке.
- Коротко: 2–5 предложений.
- Вежливо и по делу. Не выдумывай факты, даты, суммы и обещания, которых нет в письме.
- Если непонятно, чего хочет отправитель, — вежливо уточни.
- Без темы письма, без подписи и без заготовок вроде [Имя] или [дата].

Выведи только текст ответа, без пояснений."""


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

    resp = llm.invoke([SystemMessage(SYSTEM_PROMPT), HumanMessage(text)])
    return {"draft": resp.content.strip(), "attempts": state["attempts"] + 1}


def check_draft(state: DraftState) -> dict:
    draft = state["draft"]
    problems = []

    if len(draft) < 10:
        problems.append("ответ пустой или слишком короткий")
    if len(draft) > 1500:
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