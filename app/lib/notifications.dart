import 'package:flutter/foundation.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:material_ui/material_ui.dart';

import 'models.dart';
import 'ui.dart';

/// Системные уведомления: на Android — в шторке, в браузере — всплывашка Windows.
/// Показываем их, только когда приложение свёрнуто; если открыто — хватает плашки внутри.
class SystemNotifications {
  SystemNotifications._();
  static final instance = SystemNotifications._();

  final _plugin = FlutterLocalNotificationsPlugin();
  bool _ready = false;

  /// Нажали на уведомление — открыть это письмо
  void Function(int uid)? onTap;

  static bool get supported =>
      kIsWeb || defaultTargetPlatform == TargetPlatform.android;

  WebFlutterLocalNotificationsPlugin? get _web => _plugin
      .resolvePlatformSpecificImplementation<
        WebFlutterLocalNotificationsPlugin
      >();

  Future<void> init() async {
    if (!supported || _ready) return;
    try {
      final ok = await _plugin.initialize(
        settings: const InitializationSettings(
          // Белые звёздочки — значок в строке состояния (res/drawable-*/ic_notification.png)
          android: AndroidInitializationSettings('ic_notification'),
        ),
        onDidReceiveNotificationResponse: (r) {
          final uid = int.tryParse(r.payload ?? '');
          if (uid != null) onTap?.call(uid);
        },
      );
      _ready = ok ?? false;
      // Android 13+: разрешение спрашиваем сразу. В браузере так нельзя — только по кнопке (см. askPermission)
      if (!kIsWeb) {
        await _plugin
            .resolvePlatformSpecificImplementation<
              AndroidFlutterLocalNotificationsPlugin
            >()
            ?.requestNotificationsPermission();
      }
    } catch (e) {
      debugPrint('Уведомления не включились: $e');
    }
  }

  /// В браузере разрешение надо попросить по нажатию кнопки — иначе браузер откажет сам
  bool get needsPermission =>
      kIsWeb &&
      _ready &&
      _web?.permissionStatus == WebNotificationPermission.defaultPermissions;

  Future<void> askPermission() async {
    await _web?.requestNotificationsPermission();
  }

  Future<void> show(AppEvent e) async {
    if (!_ready || e.text.isEmpty) return;
    if (kIsWeb && _web?.permissionStatus != WebNotificationPermission.granted) {
      return;
    }
    try {
      await _plugin.show(
        id: e.uid, // одно уведомление на письмо: «черновик готов» заменит «новое письмо»
        title: e.text,
        body: [
          if (e.from.isNotEmpty) senderName(e.from),
          if (e.subject.isNotEmpty) e.subject,
        ].join(' — '),
        payload: '${e.uid}',
        notificationDetails: const NotificationDetails(
          android: AndroidNotificationDetails(
            'mail', // канал: пользователь может настроить его в настройках телефона
            'Почта',
            channelDescription: 'Новые письма и готовые черновики',
            importance: Importance.high,
            priority: Priority.high,
            color: Color(0xFF4F46E5),
          ),
        ),
      );
    } catch (err) {
      debugPrint('Уведомление не показалось: $err');
    }
  }
}
