import 'package:material_ui/material_ui.dart';
import 'package:shared_preferences/shared_preferences.dart';

import 'api.dart';
import 'screens/emails_screen.dart';
import 'screens/login_screen.dart';
import 'ui.dart';

void main() => runApp(SecretaryApp(api: ApiClient()));

const _tokenKey = 'session_token';
const _serverKey = 'server_url';

/// Корень приложения: помнит, вошёл ли пользователь, и показывает вход или почту
class SecretaryApp extends StatefulWidget {
  const SecretaryApp({super.key, required this.api});

  final ApiClient api;

  @override
  State<SecretaryApp> createState() => _SecretaryAppState();
}

class _SecretaryAppState extends State<SecretaryApp> {
  final _navigator = GlobalKey<NavigatorState>();
  String? _login;
  bool _restoring = true;

  @override
  void initState() {
    super.initState();
    widget.api.onUnauthorized = _onSessionLost;
    _restoreSession();
  }

  /// Токен и адрес сервера хранятся между запусками (в вебе — в localStorage браузера)
  Future<void> _restoreSession() async {
    final prefs = await SharedPreferences.getInstance();
    final server = prefs.getString(_serverKey);
    if (server != null && server.isNotEmpty) widget.api.baseUrl = server;

    final saved = prefs.getString(_tokenKey);
    String? login;
    if (saved != null) {
      widget.api.token = saved;
      try {
        login = await widget.api.me();
      } catch (_) {
        // Протух или шлюз недоступен — просто покажем вход
        await prefs.remove(_tokenKey);
        widget.api.token = null;
      }
    }
    if (!mounted) return;
    setState(() {
      _login = login;
      _restoring = false;
    });
  }

  Future<void> _onLoggedIn(String login) async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.setString(_tokenKey, widget.api.token!);
    await prefs.setString(_serverKey, widget.api.baseUrl);
    setState(() => _login = login);
  }

  Future<void> _logout() async {
    try {
      await widget.api.logout();
    } catch (_) {
      // даже если шлюз недоступен — локально всё равно выходим
    }
    await _forgetSession();
  }

  /// Сервер ответил 401 посреди работы — закрываем все экраны и показываем вход
  void _onSessionLost() {
    _navigator.currentState?.popUntil((route) => route.isFirst);
    _forgetSession();
  }

  Future<void> _forgetSession() async {
    final prefs = await SharedPreferences.getInstance();
    await prefs.remove(_tokenKey);
    if (mounted) setState(() => _login = null);
  }

  @override
  Widget build(BuildContext context) {
    final Widget home;
    if (_restoring) {
      home = const Scaffold(body: Center(child: CircularProgressIndicator()));
    } else if (_login == null) {
      home = LoginScreen(api: widget.api, onLoggedIn: _onLoggedIn);
    } else {
      home = EmailsScreen(api: widget.api, login: _login!, onLogout: _logout);
    }

    return MaterialApp(
      title: 'ИИ-секретарь',
      navigatorKey: _navigator,
      debugShowCheckedModeBanner: false,
      theme: buildTheme(Brightness.light),
      darkTheme: buildTheme(Brightness.dark),
      themeMode:
          ThemeMode.system, // тёмная тема — если она включена на телефоне/компе
      // Системные надписи (Копировать, Вставить, Назад…) — по-русски
      locale: const Locale('ru'),
      supportedLocales: const [Locale('ru')],
      localizationsDelegates: GlobalMaterialLocalizations.delegates,
      // На широком экране (браузер на компе) не растягиваем всё на всю ширину
      builder: (context, child) => ColoredBox(
        color: Theme.of(context).scaffoldBackgroundColor,
        child: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 760),
            child: child,
          ),
        ),
      ),
      home: home,
    );
  }
}
