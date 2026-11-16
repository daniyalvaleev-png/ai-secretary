import 'dart:convert';

import 'package:http/http.dart' as http;

import 'models.dart';

/// Адрес шлюза. Меняется без правки кода: flutter run --dart-define=API_URL=http://192.168.0.10:8080
const apiUrl = String.fromEnvironment(
  'API_URL',
  defaultValue: 'http://127.0.0.1:8080',
);

/// Ошибка API с текстом от сервера — его можно сразу показать пользователю
class ApiException implements Exception {
  ApiException(this.statusCode, this.message);

  final int statusCode;
  final String message;

  @override
  String toString() => message;
}

/// Клиент нашего Go-шлюза. Токен сессии приклеивается к каждому запросу.
class ApiClient {
  ApiClient({http.Client? httpClient, this.baseUrl = apiUrl})
    : _http = httpClient ?? http.Client();

  final http.Client _http;
  final String baseUrl;
  String? token;

  /// Вызывается, когда сервер ответил 401: сессия протухла или её закрыли — надо на экран входа
  void Function()? onUnauthorized;

  Map<String, String> get _headers => {
    'Content-Type': 'application/json',
    if (token != null) 'Authorization': 'Bearer $token',
  };

  Future<dynamic> _send(String method, String path, {Object? body}) async {
    final uri = Uri.parse('$baseUrl$path');
    final http.Response resp;
    try {
      resp = method == 'GET'
          ? await _http.get(uri, headers: _headers)
          : await _http.post(
              uri,
              headers: _headers,
              body: body == null ? null : jsonEncode(body),
            );
    } catch (_) {
      throw ApiException(0, 'Шлюз недоступен — он запущен? ($baseUrl)');
    }

    // Явно UTF-8, иначе кириллица превращается в кракозябры
    final text = utf8.decode(resp.bodyBytes);
    final data = text.isEmpty ? null : jsonDecode(text);

    if (resp.statusCode >= 400) {
      final message = data is Map && data['error'] is String
          ? data['error'] as String
          : 'Ошибка ${resp.statusCode}';
      if (resp.statusCode == 401 && token != null) {
        token = null;
        onUnauthorized?.call();
      }
      throw ApiException(resp.statusCode, message);
    }
    return data;
  }

  // --- Вход ---

  /// Возвращает логин; токен запоминается в клиенте
  Future<String> login(String login, String password) async {
    final data = await _send(
      'POST',
      '/api/login',
      body: {'login': login, 'password': password},
    );
    token = data['token'] as String;
    return data['login'] as String;
  }

  Future<String> me() async =>
      (await _send('GET', '/api/me'))['login'] as String;

  Future<void> logout() async {
    try {
      await _send('POST', '/api/logout');
    } finally {
      token = null;
    }
  }

  // --- Письма ---

  Future<List<Email>> emails({int limit = 100}) async {
    final data = await _send('GET', '/api/emails?limit=$limit') as List;
    return data.map((e) => Email.fromJson(e as Map<String, dynamic>)).toList();
  }

  Future<Email> email(int uid) async =>
      Email.fromJson(await _send('GET', '/api/emails/$uid'));

  /// Действие над письмом: confirm, reject, redraft, send, skip, calendar
  Future<Email> action(int uid, String action) async =>
      Email.fromJson(await _send('POST', '/api/emails/$uid/$action'));

  Future<Email> editDraft(int uid, String draft) async => Email.fromJson(
    await _send('POST', '/api/emails/$uid/draft', body: {'draft': draft}),
  );

  // --- Календарь ---

  Future<List<CalendarEvent>> calendar() async {
    final data = await _send('GET', '/api/calendar');
    return (data['events'] as List)
        .map((e) => CalendarEvent.fromJson(e as Map<String, dynamic>))
        .toList();
  }
}
