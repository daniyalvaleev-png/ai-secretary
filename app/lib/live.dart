import 'dart:async';

import 'api.dart';
import 'models.dart';

/// Слушает ленту событий шлюза и раздаёт их экранам через [stream].
///
/// Цикл такой: «что нового после N?» → сервер отвечает сразу или через ≤25 секунд →
/// раздаём события → спрашиваем снова. Если шлюз недоступен — пробуем раз в 5 секунд.
class LiveUpdates {
  LiveUpdates(this.api);

  final ApiClient api;
  final _controller = StreamController<AppEvent>.broadcast();
  bool _running = false;
  int? _last;
  Timer? _pause;
  Completer<void>? _pauseDone;

  Stream<AppEvent> get stream => _controller.stream;

  void start() {
    if (_running) return;
    _running = true;
    _loop();
  }

  void stop() {
    _running = false;
    _pause?.cancel();
    if (_pauseDone?.isCompleted == false) _pauseDone!.complete();
  }

  void dispose() {
    stop();
    _controller.close();
  }

  /// Пауза, которую можно прервать из stop() — чтобы не висели таймеры после выхода
  Future<void> _sleep(Duration d) {
    final done = _pauseDone = Completer<void>();
    _pause = Timer(d, () {
      if (!done.isCompleted) done.complete();
    });
    return done.future;
  }

  Future<void> _loop() async {
    while (_running) {
      try {
        if (_last == null) {
          // Старые события не нужны — начинаем с текущего момента
          final (_, last) = await api.events();
          _last = last;
          continue;
        }

        final (events, last) = await api.events(after: _last);
        if (!_running) break;

        if (last < _last!) {
          // Нумерация пошла сначала — шлюз перезапускали
          _controller.add(AppEvent.resync());
        }
        _last = last;
        for (final e in events) {
          _controller.add(e);
        }
        // Пустой ответ пришёл — небольшая пауза, чтобы не долбить сервер,
        // если он вдруг отвечает сразу, а не ждёт
        if (events.isEmpty) await _sleep(const Duration(seconds: 1));
      } on ApiException catch (e) {
        // Сессия кончилась — приложение само покажет вход
        if (e.statusCode == 401) break;
        await _sleep(const Duration(seconds: 5));
      } catch (_) {
        await _sleep(const Duration(seconds: 5));
      }
    }
    _running = false;
  }
}
