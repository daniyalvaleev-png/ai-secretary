import 'dart:async';

import 'package:material_ui/material_ui.dart';

import '../api.dart';
import '../live.dart';
import '../models.dart';
import '../notifications.dart';
import '../ui.dart';
import 'calendar_screen.dart';
import 'email_screen.dart';

/// Входящие: «ждут решения» или все, поиск.
/// Слушает ленту событий шлюза: новое письмо появляется сразу, а если приложение свёрнуто —
/// приходит системное уведомление.
class EmailsScreen extends StatefulWidget {
  const EmailsScreen({
    super.key,
    required this.api,
    required this.login,
    required this.onLogout,
  });

  final ApiClient api;
  final String login;
  final VoidCallback onLogout;

  @override
  State<EmailsScreen> createState() => _EmailsScreenState();
}

class _EmailsScreenState extends State<EmailsScreen> {
  final _search = TextEditingController();
  List<Email> _emails = [];
  bool _onlyWaiting = true;
  bool _loading = true;
  String? _error;
  Timer? _timer;
  Timer? _reloadSoon;

  late final _live = LiveUpdates(widget.api);
  StreamSubscription<AppEvent>? _events;
  late final AppLifecycleListener _lifecycle;
  bool _foreground = true; // приложение на экране, а не свёрнуто
  int?
  _openedUid; // какое письмо сейчас открыто — про него экран письма скажет сам

  final _notifications = SystemNotifications.instance;

  @override
  void initState() {
    super.initState();
    _load();
    // Лента событий — основной способ узнать о новом. Таймер — подстраховка, если она отвалилась
    _timer = Timer.periodic(const Duration(seconds: 60), (_) => _load());
    _events = _live.stream.listen(_onEvent);
    _live.start();

    _lifecycle = AppLifecycleListener(
      onStateChange: (s) {
        final was = _foreground;
        _foreground = s == AppLifecycleState.resumed;
        // Вернулись в приложение — освежить список
        if (_foreground && !was) _load();
      },
    );

    _notifications.onTap = _openUid;
    // После init в браузере может появиться кнопка «Включить уведомления»
    _notifications.init().then((_) {
      if (mounted) setState(() {});
    });
  }

  @override
  void dispose() {
    _timer?.cancel();
    _reloadSoon?.cancel();
    _events?.cancel();
    _live.dispose();
    _lifecycle.dispose();
    _notifications.onTap = null;
    _search.dispose();
    super.dispose();
  }

  void _onEvent(AppEvent e) {
    // Несколько событий подряд — перечитаем список один раз
    _reloadSoon?.cancel();
    _reloadSoon = Timer(const Duration(milliseconds: 300), _load);

    if (e.text.isEmpty) return;
    if (!_foreground) {
      if (e.important) _notifications.show(e);
      return;
    }
    // Приложение открыто: плашка внизу. Про открытое письмо экран письма скажет сам
    if (e.uid == _openedUid) return;
    final messenger = ScaffoldMessenger.of(context);
    messenger.hideCurrentSnackBar();
    messenger.showSnackBar(
      SnackBar(
        content: Text(
          [e.text, if (e.subject.isNotEmpty) '«${e.subject}»'].join('\n'),
        ),
        duration: const Duration(seconds: 5),
        action: e.uid == 0 || _openedUid != null
            ? null
            : SnackBarAction(
                label: 'Открыть',
                onPressed: () => _openUid(e.uid),
              ),
      ),
    );
  }

  Future<void> _load() async {
    try {
      final emails = await widget.api.emails();
      if (!mounted) return;
      setState(() {
        _emails = emails;
        _error = null;
      });
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _loading = false);
    }
  }

  void _open(Email e) => _openUid(e.uid);

  Future<void> _openUid(int uid) async {
    if (!mounted) return;
    final nav = Navigator.of(context);
    // Нажали уведомление, когда открыт другой экран, — возвращаемся к списку и открываем нужное
    nav.popUntil((r) => r.isFirst);
    _openedUid = uid;
    await nav.push(
      MaterialPageRoute(
        builder: (_) => EmailScreen(api: widget.api, uid: uid, live: _live),
      ),
    );
    if (_openedUid == uid) _openedUid = null;
    _load(); // вернулись — статус письма мог поменяться
  }

  Future<void> _confirmLogout() async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Выйти?'),
        content: Text('Ты вошёл как ${widget.login}.'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Отмена'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Выйти'),
          ),
        ],
      ),
    );
    if (ok == true) widget.onLogout();
  }

  List<Email> get _shown {
    final q = _search.text.trim().toLowerCase();
    return _emails.where((e) {
      if (_onlyWaiting && !e.needsDecision) return false;
      if (q.isEmpty) return true;
      return e.subject.toLowerCase().contains(q) ||
          e.from.toLowerCase().contains(q) ||
          e.body.toLowerCase().contains(q);
    }).toList();
  }

  @override
  Widget build(BuildContext context) {
    final waiting = _emails.where((e) => e.needsDecision).length;
    final shown = _shown;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Входящие'),
        actions: [
          // В браузере уведомления можно разрешить только по нажатию — даём кнопку
          if (_notifications.needsPermission)
            IconButton(
              tooltip: 'Включить уведомления',
              icon: const Icon(Icons.notifications_active_outlined),
              onPressed: () async {
                await _notifications.askPermission();
                if (mounted) setState(() {});
              },
            ),
          IconButton(
            tooltip: 'Календарь',
            icon: const Icon(Icons.calendar_month_outlined),
            onPressed: () => Navigator.of(context).push(
              MaterialPageRoute(
                builder: (_) => CalendarScreen(api: widget.api),
              ),
            ),
          ),
          IconButton(
            tooltip: 'Выйти (${widget.login})',
            icon: const Icon(Icons.logout),
            onPressed: _confirmLogout,
          ),
          const SizedBox(width: 4),
        ],
      ),
      body: Column(
        children: [
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 4, 16, 8),
            child: TextField(
              controller: _search,
              onChanged: (_) => setState(() {}),
              decoration: InputDecoration(
                hintText: 'Поиск по письмам',
                prefixIcon: const Icon(Icons.search),
                isDense: true,
                suffixIcon: _search.text.isEmpty
                    ? null
                    : IconButton(
                        tooltip: 'Очистить',
                        icon: const Icon(Icons.close),
                        onPressed: () => setState(_search.clear),
                      ),
              ),
            ),
          ),
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
            child: Row(
              children: [
                ChoiceChip(
                  label: Text('Ждут решения ($waiting)'),
                  selected: _onlyWaiting,
                  onSelected: (_) => setState(() => _onlyWaiting = true),
                ),
                const SizedBox(width: 8),
                ChoiceChip(
                  label: const Text('Все'),
                  selected: !_onlyWaiting,
                  onSelected: (_) => setState(() => _onlyWaiting = false),
                ),
              ],
            ),
          ),
          if (_error != null)
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 8),
              child: _ErrorBar(message: _error!, onRetry: _load),
            ),
          Expanded(
            child: _loading
                ? const Center(child: CircularProgressIndicator())
                : RefreshIndicator(
                    onRefresh: _load,
                    child: shown.isEmpty
                        ? ListView(children: [_empty()])
                        : ListView.separated(
                            padding: const EdgeInsets.fromLTRB(16, 4, 16, 24),
                            itemCount: shown.length,
                            separatorBuilder: (_, _) =>
                                const SizedBox(height: 8),
                            itemBuilder: (_, i) => _EmailCard(
                              email: shown[i],
                              onTap: () => _open(shown[i]),
                            ),
                          ),
                  ),
          ),
        ],
      ),
    );
  }

  Widget _empty() {
    if (_search.text.trim().isNotEmpty) {
      return const EmptyState(
        icon: Icons.search_off,
        title: 'Ничего не нашлось',
        subtitle: 'Попробуй другое слово',
      );
    }
    if (_onlyWaiting) {
      return EmptyState(
        icon: Icons.task_alt,
        title: 'Ничего не висит, всё разобрано',
        subtitle: 'Новые письма появятся здесь сами',
        action: _emails.isEmpty
            ? null
            : TextButton(
                onPressed: () => setState(() => _onlyWaiting = false),
                child: const Text('Показать все письма'),
              ),
      );
    }
    return const EmptyState(
      icon: Icons.inbox_outlined,
      title: 'Писем пока нет',
      subtitle: 'Как только секретарь прочитает почту, они будут здесь',
    );
  }
}

class _ErrorBar extends StatelessWidget {
  const _ErrorBar({required this.message, required this.onRetry});

  final String message;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    final s = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.fromLTRB(12, 4, 4, 4),
      decoration: BoxDecoration(
        color: s.errorContainer,
        borderRadius: BorderRadius.circular(12),
      ),
      child: Row(
        children: [
          Icon(Icons.cloud_off, color: s.onErrorContainer, size: 20),
          const SizedBox(width: 10),
          Expanded(
            child: Text(message, style: TextStyle(color: s.onErrorContainer)),
          ),
          TextButton(onPressed: onRetry, child: const Text('Повторить')),
        ],
      ),
    );
  }
}

/// Карточка письма: аватар, отправитель, время, тема, начало текста, статус
class _EmailCard extends StatelessWidget {
  const _EmailCard({required this.email, required this.onTap});

  final Email email;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final s = theme.colorScheme;
    final e = email;
    final unread = e.needsDecision;
    final preview = e.body.replaceAll(RegExp(r'\s+'), ' ').trim();

    return Card(
      clipBehavior: Clip.antiAlias,
      child: InkWell(
        onTap: onTap,
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Row(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              SenderAvatar(from: e.from, suspicious: e.suspicious),
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        Expanded(
                          child: Text(
                            senderName(e.from),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: theme.textTheme.bodyMedium?.copyWith(
                              color: s.onSurfaceVariant,
                              fontWeight: unread
                                  ? FontWeight.w600
                                  : FontWeight.w400,
                            ),
                          ),
                        ),
                        const SizedBox(width: 8),
                        Text(
                          shortDate(e.receivedAt),
                          style: theme.textTheme.bodySmall?.copyWith(
                            color: unread ? s.primary : s.onSurfaceVariant,
                            fontWeight: unread
                                ? FontWeight.w600
                                : FontWeight.w400,
                          ),
                        ),
                      ],
                    ),
                    const SizedBox(height: 2),
                    Text(
                      e.subject.isEmpty ? '(без темы)' : e.subject,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.titleMedium?.copyWith(
                        fontWeight: unread ? FontWeight.w700 : FontWeight.w500,
                      ),
                    ),
                    if (preview.isNotEmpty) ...[
                      const SizedBox(height: 2),
                      Text(
                        preview,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodyMedium?.copyWith(
                          color: s.onSurfaceVariant,
                        ),
                      ),
                    ],
                    const SizedBox(height: 10),
                    Wrap(
                      spacing: 6,
                      runSpacing: 6,
                      children: [
                        StatusPill(e.status),
                        if (e.urgency == 'высокая' && unread)
                          const UrgentPill(),
                        if (e.calendarEventId.isNotEmpty)
                          Pill(
                            label: 'В календаре',
                            bg: s.surfaceContainerHighest,
                            fg: s.onSurfaceVariant,
                            leading: Icon(
                              Icons.event_available,
                              size: 12,
                              color: s.onSurfaceVariant,
                            ),
                          ),
                      ],
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}
