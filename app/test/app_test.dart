import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:material_ui/material_ui.dart';
import 'package:secretary_app/api.dart';
import 'package:secretary_app/main.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Поддельный шлюз: хранит письма в памяти и отвечает так же, как настоящий
class FakeGateway {
  final requests = <String>[];
  bool failSend = false;
  final emails = <int, Map<String, dynamic>>{
    7: {
      'uid': 7,
      'from': 'friend@mail.ru',
      'subject': 'Кино',
      'body': 'го завтра в кино в 15:00?',
      'received_at': '2026-11-16T10:00:00+03:00',
      'urgency': 'высокая',
      'suggested_action': 'Подтвердить встречу',
      'status': 'pending',
      'draft': '',
      'suspicious': false,
      'suspicion_reason': '',
      'meeting_title': '',
      'meeting_start': '',
      'meeting_end': '',
      'calendar_note': '',
      'calendar_event_id': '',
    },
    8: {
      'uid': 8,
      'from': 'winner@prize.ru',
      'subject': 'Вы выиграли!',
      'body': 'введите данные карты',
      'urgency': 'низкая',
      'status': 'sent',
      'suspicious': true,
      'suspicion_reason': 'обещают деньги ни за что',
    },
  };

  http.Response _json(Object body, [int code = 200]) => http.Response.bytes(
    utf8.encode(jsonEncode(body)),
    code,
    headers: {'content-type': 'application/json'},
  );

  late final client = MockClient((req) async {
    final path = req.url.path;
    requests.add('${req.method} $path');

    if (path == '/api/login') {
      final b = jsonDecode(req.body);
      return b['password'] == 'secret-password'
          ? _json({'token': 'tok', 'login': 'daniyal', 'expires_at': ''})
          : _json({'error': 'неверный логин или пароль'}, 401);
    }
    if (req.headers['Authorization'] != 'Bearer tok') {
      return _json({'error': 'нужно войти'}, 401);
    }
    if (path == '/api/me') return _json({'login': 'daniyal'});
    if (path == '/api/emails') return _json(emails.values.toList());

    final m = RegExp(r'^/api/emails/(\d+)(?:/(\w+))?$').firstMatch(path);
    if (m != null) {
      final e = emails[int.parse(m[1]!)]!;
      switch (m[2]) {
        case null:
          // Отправка «упала» в фоне: письмо вернулось к готовому черновику
          if (e['status'] == 'sending') e['status'] = 'draft_ready';
          // Агент «думает»: при первом чтении после confirm черновик уже готов
          if (e['status'] == 'drafting') {
            e['status'] = 'draft_ready';
            e['draft'] = 'Привет! Давай, в 15:00 норм.';
            e['calendar_note'] = 'Календарь: во вторник в 15:00 — СВОБОДНО.';
          }
        case 'confirm':
          if (e['status'] != 'pending') {
            return _json({
              'error': 'сейчас нельзя',
              'status': e['status'],
            }, 409);
          }
          e['status'] = 'drafting';
        case 'draft':
          e['draft'] = jsonDecode(req.body)['draft'];
        case 'send':
          e['status'] = failSend ? 'sending' : 'sent';
      }
      return _json(e, m[2] == null || m[2] == 'draft' ? 200 : 202);
    }
    return _json({'error': 'нет такого'}, 404);
  });
}

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  testWidgets('вход → список → письмо → черновик → правка → отправка', (
    tester,
  ) async {
    final gw = FakeGateway();
    await tester.pumpWidget(
      SecretaryApp(
        api: ApiClient(httpClient: gw.client, baseUrl: 'http://gw'),
      ),
    );
    await tester.pumpAndSettle();

    // Неверный пароль — текст ошибки от сервера
    await tester.enterText(find.widgetWithText(TextField, 'Логин'), 'daniyal');
    await tester.enterText(find.widgetWithText(TextField, 'Пароль'), 'wrong');
    await tester.tap(find.text('Войти'));
    await tester.pumpAndSettle();
    expect(find.text('неверный логин или пароль'), findsOneWidget);

    await tester.enterText(
      find.widgetWithText(TextField, 'Пароль'),
      'secret-password',
    );
    await tester.tap(find.text('Войти'));
    await tester.pumpAndSettle();

    // Токен запомнился — после перезапуска входить заново не нужно
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('session_token'), 'tok');

    // «Ждут решения»: только «Кино», мошенническое письмо уже отправлено и скрыто
    expect(find.text('Кино'), findsOneWidget);
    expect(find.text('Вы выиграли!'), findsNothing);
    await tester.tap(find.text('Все'));
    await tester.pumpAndSettle();
    expect(find.text('Вы выиграли!'), findsOneWidget);
    await tester.tap(find.textContaining('Ждут решения'));
    await tester.pumpAndSettle();

    // Письмо → Подтвердить → экран сам дождался черновика
    await tester.tap(find.text('Кино'));
    await tester.pumpAndSettle();
    expect(find.text('го завтра в кино в 15:00?'), findsOneWidget);
    await tester.tap(find.text('Подтвердить'));
    await tester.pump();
    expect(find.text('Пишу черновик…'), findsWidgets);
    await tester.pump(
      const Duration(seconds: 3),
    ); // экран перечитывает письмо раз в 2 секунды
    await tester.pumpAndSettle();
    expect(find.text('Привет! Давай, в 15:00 норм.'), findsOneWidget);
    expect(find.textContaining('СВОБОДНО'), findsOneWidget);

    // Правим черновик и отправляем — с подтверждением, где видно, что и кому уйдёт
    await tester.enterText(
      find.widgetWithText(TextField, 'Привет! Давай, в 15:00 норм.'),
      'Привет! Давай в 15:30.',
    );
    await tester.pump();
    await tester.tap(find.text('Отправить'));
    await tester.pumpAndSettle();
    expect(find.text('Отправить ответ?'), findsOneWidget);
    expect(find.textContaining('Кому: friend@mail.ru'), findsOneWidget);
    await tester.tap(find.widgetWithText(FilledButton, 'Отправить').last);
    await tester.pumpAndSettle();

    expect(
      gw.emails[7]!['draft'],
      'Привет! Давай в 15:30.',
      reason: 'сначала сохраняется правка',
    );
    expect(
      gw.requests.indexOf('POST /api/emails/7/draft') <
          gw.requests.indexOf('POST /api/emails/7/send'),
      isTrue,
    );
    expect(find.text('Отправлено'), findsOneWidget);
  });

  testWidgets('отправка упала — приложение об этом говорит', (tester) async {
    SharedPreferences.setMockInitialValues({'session_token': 'tok'});
    final gw = FakeGateway()..failSend = true;
    gw.emails[7]!
      ..['status'] = 'draft_ready'
      ..['draft'] = 'Давай в 15:00';
    await tester.pumpWidget(
      SecretaryApp(
        api: ApiClient(httpClient: gw.client, baseUrl: 'http://gw'),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Кино'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Отправить'));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(FilledButton, 'Отправить').last);
    await tester.pump();
    expect(find.text('Отправляю…'), findsWidgets);

    await tester.pump(const Duration(seconds: 3));
    await tester.pump();
    expect(
      find.text('Не удалось отправить — попробуй ещё раз'),
      findsOneWidget,
    );
    expect(find.text('Черновик готов'), findsOneWidget);
    await tester.pump(const Duration(seconds: 5)); // дать тосту закрыться
  });

  testWidgets('подозрительное письмо: главная кнопка — «Отклонить»', (
    tester,
  ) async {
    SharedPreferences.setMockInitialValues({'session_token': 'tok'});
    final gw = FakeGateway();
    gw.emails[8]!['status'] = 'pending';
    await tester.pumpWidget(
      SecretaryApp(
        api: ApiClient(httpClient: gw.client, baseUrl: 'http://gw'),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Вы выиграли!'));
    await tester.pumpAndSettle();
    expect(find.text('Похоже на мошенничество'), findsOneWidget);
    expect(find.widgetWithText(FilledButton, 'Отклонить'), findsOneWidget);
    expect(find.text('Подтвердить'), findsNothing);
  });

  testWidgets('сессия протухла — назад на экран входа', (tester) async {
    SharedPreferences.setMockInitialValues({'session_token': 'old-token'});
    final gw = FakeGateway();
    await tester.pumpWidget(
      SecretaryApp(
        api: ApiClient(httpClient: gw.client, baseUrl: 'http://gw'),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Войти'), findsOneWidget);
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('session_token'), isNull);
  });
}
