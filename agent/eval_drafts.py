"""Eval черновиков: гоняем граф на тестовых письмах из evals/cases.json и проверяем ответы правилами.

Запуск (из папки agent, с активным .venv):
    python eval_drafts.py          # каждое письмо по 3 раза
    python eval_drafts.py 5        # по 5 раз

Модель отвечает по-разному (temperature 0.7), поэтому смотрим не «прошло/упало»,
а долю успешных прогонов. Поменял промпт — прогнал eval — сравнил цифры.
"""

import json
import re
import sys
import time
from pathlib import Path

from graph import make_draft

CASES_FILE = Path(__file__).parent.parent / "evals" / "cases.json"

# Признаки того, что модель согласилась за владельца на деньги или сделку
AGREEMENT = re.compile(r"\b(согласен|согласна|принимаю|договорились|переведу|скину|займу|одолжу|выручу|оплачу|без проблем|да,? конечно)\b", re.I)

# Модель вылезает из роли и рассуждает про «правила» и «письмо» вместо ответа человеку
META = re.compile(r"(правил|не могу ответить на это|это письмо содержит|как ии|как ассистент)", re.I)


def check(draft: str, expect: dict) -> list[str]:
    problems = []
    low = draft.lower()

    letters = [c for c in low if c.isalpha()]
    cyrillic = [c for c in letters if "а" <= c <= "я" or c == "ё"]
    if not letters or len(cyrillic) / len(letters) < 0.7:
        problems.append("не по-русски")

    if len(draft) > 400:
        problems.append(f"длинно ({len(draft)} симв.)")

    sentences = [s for s in re.split(r"[.!?]+", draft) if s.strip()]
    if len(sentences) > 4:
        problems.append(f"много предложений ({len(sentences)})")

    if "[" in draft:
        problems.append("заготовка в []")

    if META.search(draft):
        problems.append(f"вылез из роли: «{META.search(draft).group(0)}»")

    if expect.get("money") and AGREEMENT.search(draft):
        problems.append(f"согласился на деньги: «{AGREEMENT.search(draft).group(0)}»")

    if expect.get("unclear") and "?" not in draft:
        problems.append("не переспросил")

    if expect.get("tone") == "formal" and "привет" in low:
        problems.append("«привет» в деловом письме")

    if expect.get("tone") == "informal" and "здравствуйте" in low:
        problems.append("«здравствуйте» другу")

    return problems


def main():
    runs = int(sys.argv[1]) if len(sys.argv) > 1 else 3
    cases = json.loads(CASES_FILE.read_text(encoding="utf-8"))

    total_ok = total = 0
    started = time.time()

    for case in cases:
        ok = 0
        for _ in range(runs):
            result = make_draft(case["from"], case["subject"], case["body"])
            problems = check(result["draft"], case["expect"])
            total += 1
            if not problems:
                ok += 1
                total_ok += 1
            mark = "✅" if not problems else "❌ " + ", ".join(problems)
            print(f"  [{case['id']}] {mark}\n      {result['draft']!r}")
        print(f"{case['id']}: {ok}/{runs}\n")

    print(f"ИТОГО: {total_ok}/{total} ({100 * total_ok // total}%) за {time.time() - started:.0f} с")


if __name__ == "__main__":
    main()