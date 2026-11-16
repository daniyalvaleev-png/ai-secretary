import 'dart:async';

import 'package:material_ui/material_ui.dart';

import '../api.dart';
import '../models.dart';

/// Одно письмо: текст, черновик (можно поправить руками) и кнопки по статусу.
/// Пока сервер пишет черновик или отправляет, экран сам перечитывает письмо раз в 2 секунды.
class EmailScreen extends StatefulWidget {
  const EmailScreen({super.key, required this.api, required this.uid});

  final ApiClient api;
  final int uid;

  @override
  State<EmailScreen> createState() => _EmailScreenState();
}

class _EmailScreenState extends State<EmailScreen> {
  final _draft = TextEditingController();
  Email? _email;
  String? _error;
  bool _working = false; // ждём ответа на нажатую кнопку
  Timer? _poll;

  @override
  void initState() {
    super.initState();
    _load();
  }

  @override
  void dispose() {
    _poll?.cancel();
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

  void _toast(String text) =>
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(text)));

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

  bool get _draftEdited =>
      _email != null && _draft.text.trim() != _email!.draft.trim();

  Future<void> _send() async {
    final e = _email!;
    final ok = await showDialog<bool>(
      context: context,
      builder: (ctx) => AlertDialog(
        title: const Text('Отправить ответ?'),
        content: Text('Кому: ${e.from}\n\n${_draft.text.trim()}'),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(ctx, false),
            child: const Text('Отмена'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(ctx, true),
            child: const Text('Отправить'),
          ),
        ],
      ),
    );
    if (ok != true) return;

    await _run(() async {
      if (_draftEdited) {
        await widget.api.editDraft(
          e.uid,
          _draft.text,
        ); // сначала сохраняем правки — уйдёт именно исправленный текст
      }
      return widget.api.action(e.uid, 'send');
    });
  }

  Future<void> _redraft() async {
    if (_draftEdited) {
      final ok = await showDialog<bool>(
        context: context,
        builder: (ctx) => AlertDialog(
          title: const Text('Переписать черновик?'),
          content: const Text(
            'Твои правки пропадут — агент напишет новый вариант.',
          ),
          actions: [
            TextButton(
              onPressed: () => Navigator.pop(ctx, false),
              child: const Text('Отмена'),
            ),
            FilledButton(
              onPressed: () => Navigator.pop(ctx, true),
              child: const Text('Переписать'),
            ),
          ],
        ),
      );
      if (ok != true) return;
    }
    await _run(() => widget.api.action(_email!.uid, 'redraft'));
  }

  Future<void> _addToCalendar() async {
    await _run(() => widget.api.action(_email!.uid, 'calendar'));
    if (!mounted) return;
    _toast('Добавляю в календарь…');
    // Событие создаётся в фоне, а статус письма при этом не меняется — перечитаем сами, чтобы увидеть «Уже в календаре»
    Future.delayed(const Duration(seconds: 2), () {
      if (mounted) _load();
    });
  }

  @override
  Widget build(BuildContext context) {
    final e = _email;
    return Scaffold(
      appBar: AppBar(title: Text(e?.subject ?? 'Письмо')),
      body: e == null
          ? Center(
              child: _error != null
                  ? Text(_error!)
                  : const CircularProgressIndicator(),
            )
          : ListView(
              padding: const EdgeInsets.all(16),
              children: [
                _header(e),
                if (e.suspicious) _suspiciousBanner(e),
                const SizedBox(height: 12),
                _section('Письмо', SelectableText(e.body.trim())),
                if (e.suggestedAction.isNotEmpty)
                  _section('Что советует секретарь', Text(e.suggestedAction)),
                if (e.calendarNote.isNotEmpty || e.meetingStart.isNotEmpty)
                  _calendar(e),
                if (e.draft.isNotEmpty || e.status == 'draft_ready')
                  _draftEditor(e),
                const SizedBox(height: 16),
                _actions(e),
              ],
            ),
    );
  }

  Widget _header(Email e) {
    final theme = Theme.of(context);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(e.from, style: theme.textTheme.titleMedium),
        const SizedBox(height: 4),
        Wrap(
          spacing: 8,
          children: [
            Chip(label: Text(statusLabels[e.status] ?? e.status)),
            if (e.urgency.isNotEmpty)
              Chip(label: Text('Срочность: ${e.urgency}')),
          ],
        ),
      ],
    );
  }

  Widget _suspiciousBanner(Email e) {
    final scheme = Theme.of(context).colorScheme;
    return Card(
      color: scheme.errorContainer,
      child: ListTile(
        leading: Icon(Icons.gpp_maybe, color: scheme.onErrorContainer),
        title: Text(
          'Похоже на мошенничество',
          style: TextStyle(color: scheme.onErrorContainer),
        ),
        subtitle: Text(
          '${e.suspicionReason}\nНе переходи по ссылкам и ничего не отправляй, пока не проверишь.',
          style: TextStyle(color: scheme.onErrorContainer),
        ),
      ),
    );
  }

  Widget _section(String title, Widget child) {
    return Padding(
      padding: const EdgeInsets.only(top: 12),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(title, style: Theme.of(context).textTheme.labelLarge),
          const SizedBox(height: 4),
          child,
        ],
      ),
    );
  }

  Widget _calendar(Email e) {
    return _section(
      'Календарь',
      Text(
        [
          if (e.calendarNote.isNotEmpty) e.calendarNote,
          if (e.meetingStart.isNotEmpty)
            '📅 ${e.meetingTitle}: ${e.meetingStart}–${e.meetingEnd.length >= 16 ? e.meetingEnd.substring(11) : e.meetingEnd}',
          if (e.calendarEventId.isNotEmpty) '✅ Уже в календаре',
        ].join('\n'),
      ),
    );
  }

  Widget _draftEditor(Email e) {
    final editable = e.status == 'draft_ready' && !_working;
    return _section(
      'Черновик ответа',
      TextField(
        controller: _draft,
        enabled: editable,
        maxLines: null,
        minLines: 3,
        decoration: InputDecoration(
          border: const OutlineInputBorder(),
          helperText: editable ? 'Можно поправить перед отправкой' : null,
        ),
        onChanged: (_) =>
            setState(() {}), // чтобы кнопки знали, что черновик правили
      ),
    );
  }

  Widget _actions(Email e) {
    if (e.busy) {
      return Row(
        children: [
          const SizedBox(
            width: 20,
            height: 20,
            child: CircularProgressIndicator(strokeWidth: 2),
          ),
          const SizedBox(width: 12),
          Text(statusLabels[e.status] ?? e.status),
        ],
      );
    }

    final buttons = <Widget>[
      if (e.status == 'pending' && !e.suspicious) ...[
        FilledButton.icon(
          icon: const Icon(Icons.edit_note),
          label: const Text('Подтвердить'),
          onPressed: () => _run(() => widget.api.action(e.uid, 'confirm')),
        ),
        OutlinedButton.icon(
          icon: const Icon(Icons.close),
          label: const Text('Отклонить'),
          onPressed: () => _run(() => widget.api.action(e.uid, 'reject')),
        ),
      ],
      // Подозрительное: главная кнопка — «Отклонить», чтобы не подтвердить мошенника по привычке
      if (e.status == 'pending' && e.suspicious) ...[
        FilledButton.icon(
          icon: const Icon(Icons.close),
          label: const Text('Отклонить'),
          onPressed: () => _run(() => widget.api.action(e.uid, 'reject')),
        ),
        TextButton(
          onPressed: () => _run(() => widget.api.action(e.uid, 'confirm')),
          child: const Text('Всё равно ответить'),
        ),
      ],
      if (e.status == 'draft_ready') ...[
        FilledButton.icon(
          icon: const Icon(Icons.send),
          label: const Text('Отправить'),
          onPressed: _draft.text.trim().isEmpty ? null : _send,
        ),
        OutlinedButton.icon(
          icon: const Icon(Icons.refresh),
          label: const Text('Переписать'),
          onPressed: _redraft,
        ),
      ],
      if (e.status == 'draft_failed')
        FilledButton.icon(
          icon: const Icon(Icons.refresh),
          label: const Text('Повторить'),
          onPressed: () => _run(() => widget.api.action(e.uid, 'redraft')),
        ),
      if (e.status == 'draft_ready' || e.status == 'draft_failed')
        TextButton(
          onPressed: () => _run(() => widget.api.action(e.uid, 'skip')),
          child: const Text('Не отвечать'),
        ),
      if (e.canAddToCalendar)
        OutlinedButton.icon(
          icon: const Icon(Icons.event),
          label: const Text('В календарь'),
          onPressed: _addToCalendar,
        ),
    ];

    if (buttons.isEmpty) return const SizedBox.shrink();
    return AbsorbPointer(
      absorbing: _working, // пока ждём ответ — кнопки не жмутся второй раз
      child: Opacity(
        opacity: _working ? 0.5 : 1,
        child: Wrap(spacing: 8, runSpacing: 8, children: buttons),
      ),
    );
  }
}
