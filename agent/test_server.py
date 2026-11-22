"""Тест пропуска к агенту: без ключа — 401, с ключом — черновик. Модель не нужна, граф подменён.

Запуск (из папки agent, с активным .venv):
    python -m unittest -v test_server
"""

import os
import unittest

os.environ["AGENT_API_KEY"] = "test-key"  # до импорта server — он читает ключ при запуске

from fastapi.testclient import TestClient  # noqa: E402

import server  # noqa: E402

LETTER = {"uid": 7, "sender": "a@b.ru", "subject": "Кино", "body": "го в кино?"}


def fake_make_draft(sender, subject, body):
    return {"draft": "Привет! Давай.", "attempts": 1, "problems": "", "meeting": None, "calendar_note": ""}


class TestAgentKey(unittest.TestCase):
    def setUp(self):
        self._orig = server.make_draft
        server.make_draft = fake_make_draft
        self.client = TestClient(server.app)

    def tearDown(self):
        server.make_draft = self._orig

    def test_without_key(self):
        self.assertEqual(self.client.post("/draft", json=LETTER).status_code, 401)

    def test_wrong_key(self):
        r = self.client.post("/draft", json=LETTER, headers={"Authorization": "Bearer wrong-key"})
        self.assertEqual(r.status_code, 401)

    def test_right_key(self):
        r = self.client.post("/draft", json=LETTER, headers={"Authorization": "Bearer test-key"})
        self.assertEqual(r.status_code, 200)
        self.assertEqual(r.json()["draft"], "Привет! Давай.")

    def test_health_is_open(self):
        self.assertEqual(self.client.get("/health").status_code, 200)

    def test_no_docs(self):
        # Внутренний сервис не рассказывает о себе
        self.assertEqual(self.client.get("/docs").status_code, 404)
        self.assertEqual(self.client.get("/openapi.json").status_code, 404)

    def test_too_long_letter(self):
        big = dict(LETTER, body="а" * 30000)
        r = self.client.post("/draft", json=big, headers={"Authorization": "Bearer test-key"})
        self.assertEqual(r.status_code, 422)


if __name__ == "__main__":
    unittest.main()
