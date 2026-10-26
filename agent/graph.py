"""LangGraph-граф: пишет черновик ответа на письмо и сам проверяет его.

START -> write_draft -> check_draft -> есть проблемы и попытки не кончились?
                                          да  -> снова write_draft
                                          нет -> END
"""

import os
import re
from typing import TypedDict

from langchain_core.messages import AIMessage, HumanMessage, SystemMessage
from langchain_ollama import ChatOllama
from langgraph.graph import END, START, StateGraph

MAX_ATTEMPTS = 3

llm = ChatOllama(
    model=os.getenv("DRAFT_MODEL", "granite4.2:3b"),
    base_url=os.getenv("OLLAMA_URL", "http://localhost:11434"),
    temperature=0.7,
    keep_alive="30m",
    reasoning=False,  # granite4.2 думает перед ответом — для коротких черновиков это лишние минуты
)

SYSTEM_PROMPT = """Ты пишешь ответ на письмо от имени владельца почты — от первого лица, как будто он сам отвечает живому человеку.

Правила:
- Только русский язык, простые живые фразы.
- Коротко: 1–3 предложения.
- Тон зеркаль отправителя. Пишет «Здравствуйте», «Добрый день» или это работа, компания, начальник — начинай со «Здравствуйте» и пиши на «вы». «Привет» и «ты» — только если отправитель сам так пишет.
- Используй только факты из письма. Не придумывай сроки, даты и обещания: если спрашивают «когда» — ответь, что уточнишь и напишешь.
- Никогда не соглашайся от имени владельца на деньги, займы, сделки и обязательства — ответь, что посмотришь и вернёшься с ответом.
- Если непонятно, чего хотят, — коротко переспроси.
- Не рассуждай про само письмо, правила или свою роль. Просто ответь как человек.
- Не цитируй грубости. Без темы, подписи и заготовок вроде [Имя].

Выведи только текст ответа."""

# Few-shot: примеры «письмо → хороший ответ». Маленькие модели копируют примеры лучше, чем следуют правилам,
# поэтому здесь по примеру на каждый тип ситуации, где модель косячила
EXAMPLES = [
    HumanMessage("От: ivan@mail.ru\nТема: созвон\n\nздарова, го созвонимся завтра в 18?"),
    AIMessage("Привет! Давай, завтра в 18 норм. Позвоню сам."),
    HumanMessage("От: hr@company.ru\nТема: Собеседование\n\nЗдравствуйте! Приглашаем вас на собеседование в четверг. Удобно ли вам?"),
    AIMessage("Здравствуйте! Спасибо за приглашение. В четверг удобно — подскажите, пожалуйста, время и формат встречи."),
    HumanMessage("От: director@firma.ru\nТема: Презентация\n\nДобрый день. Когда пришлёте презентацию для клиента?"),
    AIMessage("Здравствуйте! Уточню, на каком она этапе, и сегодня напишу точный срок."),
    HumanMessage("От: petya@mail.ru\nТема: долг\n\nслушай, скинь 10 тыщ до зарплаты, верну в понедельник"),
    AIMessage("Привет! Гляну, что у меня по деньгам, и отпишусь чуть позже."),
    HumanMessage("От: kolya@mail.ru\nТема: ыы\n\nну а дальше то что"),
    AIMessage("Привет! Не совсем понял, о чём ты, — можешь объяснить подробнее?"),
]

# Правила-ограждения в коде: промпт — это просьба, а проверка — гарантия
FORMAL = re.compile(r"(здравствуйте|добрый (день|вечер|утро)|уважаем)", re.I)
MONEY = re.compile(r"(руб|₽|деньг|денег|миллион|тыс|\d\s?к\b|перевед|переведу|займ|одолж|оплат|сделк|договор)", re.I)
AGREEMENT = re.compile(r"\b(согласен|согласна|принимаю|договорились|переведу|скину|займу|одолжу|выручу|оплачу|без проблем|да,? конечно)\b", re.I)
META = re.compile(r"(правил|не могу ответить на это|это письмо содержит|как ии|как ассистент)", re.I)


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
    body = state["body"]
    problems = []

    if len(draft) < 10:
        problems.append("ответ пустой или слишком короткий")

    sentences = [s for s in re.split(r"[.!?]+", draft) if s.strip()]
    if len(draft) > 400 or len(sentences) > 4:
        problems.append("ответ слишком длинный — нужно 1–3 предложения")

    letters = [c for c in draft.lower() if c.isalpha()]
    cyrillic = [c for c in letters if "а" <= c <= "я" or c == "ё"]
    if letters and len(cyrillic) / len(letters) < 0.7:
        problems.append("ответ не на русском языке")

    if "[" in draft and "]" in draft:
        problems.append("в ответе остались заготовки в квадратных скобках")

    if FORMAL.search(body) and "привет" in draft.lower():
        problems.append("отправитель пишет официально — начни со «Здравствуйте» и пиши на «вы»")

    if MONEY.search(body) and AGREEMENT.search(draft):
        problems.append("нельзя соглашаться на деньги или сделку за владельца — ответь, что посмотришь и вернёшься с ответом")

    if META.search(draft):
        problems.append("не рассуждай про письмо и правила — просто ответь как человек")

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