import 'dart:async';

import 'package:material_ui/material_ui.dart';

import '../api.dart';
import '../live.dart';
import '../models.dart';
import '../ui.dart';

/// Одно письмо: текст, совет секретаря, календарь, черновик (можно поправить руками)
/// и кнопки внизу экрана — всегда под пальцем.
/// Обновляется само: по ленте событий (если она есть) и раз в 2 секунды, пока сервер
/// пишет черновик или отправляет.
class EmailScreen extends StatefulWidget {
  const EmailScreen({
    super.key,
    required this.api,
    required this.uid,
    this.live,
  });

  final ApiClient api;
  final int uid;
  final LiveUpdates? live;

  @override
  State<EmailScreen> createState() => _EmailScreenState();
}

class _EmailScreenState extends State<EmailScreen> {
  final _draft = TextEditingController();
  Email? _email;
  String? _error;
  bool _working = false; // ждём ответа на нажатую кнопку
  Timer? _poll;
  StreamSubscription<AppEvent>? _events;

  @override
  void initState() {
    super.initState();
    _load();
    // Письмо поменялось (черновик готов, нажали кнопку в Telegram…) — перечитываем сразу
    _events = widget.live?.stream.listen((e) {
      if (e.uid == widget.uid || e.type == 'resync') _load();
    });
  }

  @override
  void dispose() {
    _poll?.cancel();
    _events?.cancel();
    _draft.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    try {
      _apply(await widget.api.email(widget.uid));
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  /// Показать свежую версию письма и решить, нужно ли дальше её перечитывать
  void _apply(Email e) {
    if (!mounted) return;
    final before = _email?.status;
    final draftChanged = e.draft != _email?.draft;

    // Фоновая работа закончилась — говорим, чем:
    // иначе неудачная отправка выглядит как «ничего не произошло»
    final result = switch ((before, e.status)) {
      ('sending', 'sent') => 'Ответ отправлен',
      ('sending', 'draft_ready') => 'Не удалось отправить — попробуй ещё раз',
      ('drafting', 'draft_failed') => 'Черновик не получился — можно повторить',
      _ => null,
    };
    if (result != null) _toast(result);

    setState(() {
      _email = e;
      _error = null;
    });
    // Правки пользователя не затираем, пока черновик не сменился на сервере
    if (draftChanged) _draft.text = e.draft;

    _poll?.cancel();
    if (e.busy) {
      _poll = Timer(const Duration(seconds: 2), _load);
    }
  }

  void _toast(String text) {
    ScaffoldMessenger.of(context)
      ..hideCurrentSnackBar()
      ..showSnackBar(SnackBar(content: Text(text)));
  }

  Future<void> _run(Future<Email> Function() call) async {
    setState(() => _working = true);
    try {
      _apply(await call());
    } on ApiException catch (e) {
      _toast(e.message);
      _load(); // 409 — значит, письмо уже в другом статусе: покажем актуальный
    } finally {
      if (mounted) setState(() => _working = false);
    }
  }

  void _act(String action) => _run(() => widget.api.action(widget.uid, action));

  bool get _draftEdited =>
      _email != null && _draft.text.trim() != _email!.draft.trim();

  Future<bool> _ask(String title, String text, String yes) async {
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: Text(title),
        content: SingleChildScrollView(child: Text(text)),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Отмена'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: Text(yes),
          ),
        ],
      ),
    );
    return ok == true;
  }

  Future<void> _send() async {
    final e = _email!;
    final ok = await _ask(
      'Отправить ответ?',
      'Кому: ${e.from}\n\n${_draft.text.trim()}',
      'Отправить',
    );
    if (!ok) return;

    await _run(() async {
      if (_draftEdited) {
        // Сначала сохраняем правки — уйдёт именно исправленный текст
        await widget.api.editDraft(e.uid, _draft.text);
      }
      return widget.api.action(e.uid, 'send');
    });
  }

  Future<void> _redraft() async {
    if (_draftEdited &&
        !await _ask(
          'Переписать черновик?',
          'Твои правки пропадут — агент напишет новый вариант.',
          'Переписать',
        )) {
      return;
    }
    _act('redraft');
  }

  Future<void> _addToCalendar() async {
    await _run(() => widget.api.action(widget.uid, 'calendar'));
    if (!mounted) return;
    _toast('Добавляю в календарь…');
    // Событие создаётся в фоне, а статус письма при этом не меняется —
    // перечитаем сами, чтобы увидеть «Уже в календаре»
    Future.delayed(const Duration(seconds: 2), () {
      if (mounted) _load();
    });
  }

  @override
  Widget build(BuildContext context) {
    final e = _email;
    return Scaffold(
      appBar: AppBar(),
      body: e == null
          ? Center(
              child: _error != null
                  ? EmptyState(
                      icon: Icons.cloud_off,
                      title: _error!,
                      action: TextButton(
                        onPressed: _load,
                        child: const Text('Повторить'),
                      ),
                    )
                  : const CircularProgressIndicator(),
            )
          : ListView(
              padding: const EdgeInsets.fromLTRB(16, 0, 16, 24),
              children: [
                _header(e),
                if (e.suspicious) ...[
                  const SizedBox(height: 12),
                  _suspiciousBanner(e),
                ],
                const SizedBox(height: 12),
                SectionCard(
                  icon: Icons.mail_outline,
                  title: 'Письмо',
                  child: SelectableText(
                    e.body.trim().isEmpty ? '(пустое письмо)' : e.body.trim(),
                    style: Theme.of(context).textTheme.bodyLarge
                        ?.copyWith(height: 1.4),
                  ),
                ),
                if (e.suggestedAction.isNotEmpty) ...[
                  const SizedBox(height: 12),
                  SectionCard(
                    icon: Icons.auto_awesome,
                    title: 'Секретарь советует',
                    child: Text(e.suggestedAction),
                  ),
                ],
                if (e.calendarNote.isNotEmpty || e.meetingStart.isNotEmpty) ...[
                  const SizedBox(height: 12),
                  _calendar(e),
                ],
                if (e.draft.isNotEmpty || e.status == 'draft_ready') ...[
                  const SizedBox(height: 12),
                  _draftEditor(e),
                ],
              ],
            ),
      bottomNavigationBar: e == null ? null : _actions(e),
    );
  }

  Widget _header(Email e) {
    final theme = Theme.of(context);
    final s = theme.colorScheme;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          e.subject.isEmpty ? '(без темы)' : e.subject,
          style: theme.textTheme.headlineSmall?.copyWith(
            fontWeight: FontWeight.w700,
          ),
        ),
        const SizedBox(height: 8),
        Wrap(
          spacing: 6,
          runSpacing: 6,
          children: [
            StatusPill(e.status),
            if (e.urgency == 'высокая') const UrgentPill(),
          ],
        ),
        const SizedBox(height: 14),
        Row(
          children: [
            SenderAvatar(from: e.from, suspicious: e.suspicious, size: 40),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    senderName(e.from),
                    style: theme.textTheme.titleSmall?.copyWith(
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                  Text(
                    [
                      senderAddress(e.from),
                      if (e.receivedAt != null) fullDate(e.receivedAt),
                    ].join(' · '),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: s.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            ),
          ],
        ),
      ],
    );
  }

  Widget _suspiciousBanner(Email e) {
    final s = Theme.of(context).colorScheme;
    return Container(
      padding: const EdgeInsets.all(14),
      decoration: BoxDecoration(
        color: s.errorContainer,
        borderRadius: BorderRadius.circular(18),
      ),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Icon(Icons.gpp_maybe, color: s.onErrorContainer),
          const SizedBox(width: 12),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  e.status == 'spam'
                      ? 'Приглушено как спам'
                      : 'Похоже на мошенничество',
                  style: TextStyle(
                    color: s.onErrorContainer,
                    fontWeight: FontWeight.w700,
                  ),
                ),
                const SizedBox(height: 4),
                Text(
                  [
                    if (e.suspicionReason.isNotEmpty) e.suspicionReason,
                    'Не переходи по ссылкам и ничего не отправляй, пока не проверишь.',
                  ].join('\n'),
                  style: TextStyle(color: s.onErrorContainer),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }

  Widget _calendar(Email e) {
    final theme = Theme.of(context);
    final s = theme.colorScheme;
    final start = parseLocal(e.meetingStart);
    final end = parseLocal(e.meetingEnd);
    final inCalendar = e.calendarEventId.isNotEmpty;

    return SectionCard(
      icon: Icons.event_outlined,
      title: 'Календарь',
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          if (start != null)
            Container(
              padding: const EdgeInsets.all(12),
              decoration: BoxDecoration(
                color: s.primaryContainer.withValues(alpha: 0.5),
                borderRadius: BorderRadius.circular(12),
              ),
              child: Row(
                children: [
                  Icon(Icons.schedule, color: s.primary, size: 20),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          e.meetingTitle.isEmpty ? 'Встреча' : e.meetingTitle,
                          style: const TextStyle(fontWeight: FontWeight.w600),
                        ),
                        Text(
                          '${dayTitle(start)}, ${hhmm(start)}'
                          '${end != null ? '–${hhmm(end)}' : ''}',
                          style: theme.textTheme.bodySmall,
                        ),
                      ],
                    ),
                  ),
                  if (inCalendar)
                    Icon(Icons.check_circle, color: s.primary, size: 22),
                ],
              ),
            ),
          if (e.calendarNote.isNotEmpty) ...[
            if (start != null) const SizedBox(height: 10),
            Text(e.calendarNote),
          ],
          if (inCalendar) ...[
            const SizedBox(height: 8),
            Text(
              '✅ Уже в календаре',
              style: TextStyle(color: s.primary, fontWeight: FontWeight.w600),
            ),
          ] else if (e.canAddToCalendar) ...[
            const SizedBox(height: 12),
            FilledButton.tonalIcon(
              onPressed: _working ? null : _addToCalendar,
              icon: const Icon(Icons.event_available),
              label: const Text('В календарь'),
            ),
          ],
        ],
      ),
    );
  }

  Widget _draftEditor(Email e) {
    final editable = e.status == 'draft_ready' && !_working;
    return SectionCard(
      icon: Icons.edit_note,
      title: 'Черновик ответа',
      child: TextField(
        controller: _draft,
        enabled: editable,
        maxLines: null,
        minLines: 4,
        style: Theme.of(context).textTheme.bodyLarge?.copyWith(height: 1.4),
        decoration: InputDecoration(
          helperText: editable ? 'Можно поправить перед отправкой' : null,
        ),
        // чтобы кнопки знали, что черновик правили
        onChanged: (_) => setState(() {}),
      ),
    );
  }

  /// Нижняя панель: что можно сделать с письмом в его текущем статусе
  Widget? _actions(Email e) {
    final s = Theme.of(context).colorScheme;

    Widget bar(List<Widget> children) => Material(
      color: Theme.of(context).cardTheme.color,
      child: SafeArea(
        top: false,
        child: Container(
          decoration: BoxDecoration(
            border: Border(
              top: BorderSide(color: s.outlineVariant.withValues(alpha: 0.4)),
            ),
          ),
          padding: const EdgeInsets.fromLTRB(16, 12, 16, 12),
          child: AbsorbPointer(
            absorbing:
                _working, // пока ждём ответ — кнопки не жмутся второй раз
            child: AnimatedOpacity(
              duration: const Duration(milliseconds: 150),
              opacity: _working ? 0.5 : 1,
              child: Row(children: children),
            ),
          ),
        ),
      ),
    );

    const gap = SizedBox(width: 10);

    if (e.busy) {
      return bar([
        const SizedBox(
          width: 20,
          height: 20,
          child: CircularProgressIndicator(strokeWidth: 2),
        ),
        const SizedBox(width: 12),
        Expanded(child: Text(statusLabel(e.status))),
      ]);
    }

    return switch (e.status) {
      'pending' when e.suspicious => bar([
        // Главная кнопка — «Отклонить», чтобы не подтвердить мошенника по привычке
        Expanded(
          child: TextButton(
            onPressed: () => _act('confirm'),
            child: const Text('Всё равно ответить'),
          ),
        ),
        gap,
        Expanded(
          child: FilledButton.icon(
            icon: const Icon(Icons.close),
            label: const Text('Отклонить'),
            onPressed: () => _act('reject'),
          ),
        ),
      ]),
      'pending' => bar([
        Expanded(
          child: OutlinedButton.icon(
            icon: const Icon(Icons.close),
            label: const Text('Отклонить'),
            onPressed: () => _act('reject'),
          ),
        ),
        gap,
        Expanded(
          child: FilledButton.icon(
            icon: const Icon(Icons.edit_note),
            label: const Text('Подтвердить'),
            onPressed: () => _act('confirm'),
          ),
        ),
      ]),
      'draft_ready' => bar([
        _moreMenu(),
        Expanded(
          child: OutlinedButton(
            onPressed: _redraft,
            child: const Text('Переписать'),
          ),
        ),
        gap,
        Expanded(
          child: FilledButton.icon(
            icon: const Icon(Icons.send),
            label: const Text('Отправить'),
            onPressed: _draft.text.trim().isEmpty ? null : _send,
          ),
        ),
      ]),
      'draft_failed' => bar([
        Expanded(
          child: OutlinedButton(
            onPressed: () => _act('skip'),
            child: const Text('Не отвечать'),
          ),
        ),
        gap,
        Expanded(
          child: FilledButton.icon(
            icon: const Icon(Icons.refresh),
            label: const Text('Повторить'),
            onPressed: () => _act('redraft'),
          ),
        ),
      ]),
      // Защита от бомбера могла ошибиться — возвращаем письмо в «ждут решения»
      'spam' => bar([
        Expanded(
          child: OutlinedButton.icon(
            icon: const Icon(Icons.restore),
            label: const Text('Не спам'),
            onPressed: () => _act('not_spam'),
          ),
        ),
      ]),
      _ => null, // письмо закрыто — делать нечего
    };
  }

  Widget _moreMenu() => PopupMenuButton<String>(
    tooltip: 'Ещё',
    icon: const Icon(Icons.more_vert),
    onSelected: _act,
    itemBuilder: (_) => const [
      PopupMenuItem(
        value: 'skip',
        child: ListTile(
          leading: Icon(Icons.do_not_disturb_on_outlined),
          title: Text('Не отвечать'),
          contentPadding: EdgeInsets.zero,
        ),
      ),
    ],
  );
}
