"""Eval извлечения встреч: правильно ли модель достаёт дату и время из писем.

Запуск (из папки agent, с активным .venv):
    python eval_meetings.py

«Сегодня» зафиксировано на понедельник 2026-10-26, чтобы ожидания в cases.json не протухали.
"""

import json
import time
from datetime import date
from pathlib import Path

from meeting import extract_meeting

TODAY = date(2026, 10, 26)
CASES_FILE = Path(__file__).parent.parent / "evals" / "cases.json"


def main():
    cases = [c for c in json.loads(CASES_FILE.read_text(encoding="utf-8")) if "meeting" in c["expect"]]
    ok = 0
    started = time.time()

    for case in cases:
        want = case["expect"]["meeting"]
        got = extract_meeting(case["from"], case["subject"], case["body"], TODAY)

        if want is None:
            passed = got is None
        else:
            passed = got is not None and got["date"] == want["date"] and got["time"] == want.get("time", "")

        ok += passed
        print(f"{'✅' if passed else '❌'} [{case['id']}] ждали {want}, получили {got}")

    print(f"\nИТОГО: {ok}/{len(cases)} за {time.time() - started:.0f} с")


if __name__ == "__main__":
    main()