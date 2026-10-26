"""Проверка связи агент → календарь по MCP. Модель не нужна, только сам сервер календаря.

Запуск (из папки agent, с активным .venv и запущенным calendar-mcp):
    $env:CALENDAR_API_KEY = "ключ_из_calendar-mcp/.env"
    python check_calendar.py
"""

import asyncio
import os

from langchain_mcp_adapters.client import MultiServerMCPClient


async def main():
    client = MultiServerMCPClient({
        "calendar": {
            "transport": "streamable_http",
            "url": os.getenv("CALENDAR_URL", "http://127.0.0.1:8095/mcp"),
            "headers": {"Authorization": "Bearer " + os.environ["CALENDAR_API_KEY"]},
        }
    })

    # MCP-инструменты превращаются в обычные LangChain-инструменты — их можно звать руками или отдать модели
    tools = {t.name: t for t in await client.get_tools()}
    print("🔧 Инструменты:", ", ".join(tools))

    now = await tools["now"].ainvoke({})
    print("\n🕐 now:", now[0]["text"])

    created = await tools["create_event"].ainvoke({
        "title": "Проверка календаря",
        "start": "2030-01-15 14:00",
        "duration_minutes": 30,
    })
    print("\n➕ create_event:", created[0]["text"])

    slots = await tools["find_free_slots"].ainvoke({"date": "2030-01-15", "duration_minutes": 60})
    print("\n🟢 find_free_slots:", slots[0]["text"])

    bad = await tools["create_event"].ainvoke({"title": "кривое", "start": "послезавтра в 2"})
    print("\n⚠️ кривое время (модель увидит этот текст и исправится):", bad[0]["text"])


asyncio.run(main())