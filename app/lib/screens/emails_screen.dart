import 'dart:async';

import 'package:material_ui/material_ui.dart';

import '../api.dart';
import '../models.dart';
import 'calendar_screen.dart';
import 'email_screen.dart';

/// Список писем: «ждут решения» или все. Сам обновляется раз в 15 секунд.
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
  List<Email> _emails = [];
  bool _onlyWaiting = true;
  bool _loading = true;
  String? _error;
  Timer? _timer;

  @override
  void initState() {
    super.initState();
    _load();
    _timer = Timer.periodic(const Duration(seconds: 15), (_) => _load());
  }

  @override
  void dispose() {
    _timer?.cancel();
    super.dispose();
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

  Future<void> _open(Email e) async {
    await Navigator.of(context).push(
      MaterialPageRoute(
        builder: (_) => EmailScreen(api: widget.api, uid: e.uid),
      ),
    );
    _load(); // вернулись — статус письма мог поменяться
  }

  @override
  Widget build(BuildContext context) {
    final shown = _onlyWaiting
        ? _emails.where((e) => e.needsDecision).toList()
        : _emails;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Почта'),
        actions: [
          IconButton(
            tooltip: 'Календарь',
            icon: const Icon(Icons.calendar_month),
            onPressed: () => Navigator.of(context).push(
              MaterialPageRoute(
                builder: (_) => CalendarScreen(api: widget.api),
              ),
            ),
          ),
          IconButton(
            tooltip: 'Выйти (${widget.login})',
            icon: const Icon(Icons.logout),
            onPressed: widget.onLogout,
          ),
        ],
      ),
      body: Column(
        children: [
          Padding(
            padding: const EdgeInsets.all(12),
            child: SegmentedButton<bool>(
              segments: [
                ButtonSegment(
                  value: true,
                  label: Text(
                    'Ждут решения (${_emails.where((e) => e.needsDecision).length})',
                  ),
                ),
                const ButtonSegment(value: false, label: Text('Все')),
              ],
              selected: {_onlyWaiting},
              onSelectionChanged: (s) => setState(() => _onlyWaiting = s.first),
            ),
          ),
          if (_error != null)
            MaterialBanner(
              content: Text(_error!),
              actions: [
                TextButton(onPressed: _load, child: const Text('Повторить')),
              ],
            ),
          Expanded(
            child: _loading
                ? const Center(child: CircularProgressIndicator())
                : RefreshIndicator(
                    onRefresh: _load,
                    child: shown.isEmpty
                        ? ListView(
                            children: const [
                              SizedBox(height: 120),
                              Center(
                                child: Text(
                                  '🎉 Ничего не висит, всё разобрано',
                                ),
                              ),
                            ],
                          )
                        : ListView.separated(
                            itemCount: shown.length,
                            separatorBuilder: (_, _) =>
                                const Divider(height: 1),
                            itemBuilder: (_, i) => _EmailTile(
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
}

class _EmailTile extends StatelessWidget {
  const _EmailTile({required this.email, required this.onTap});

  final Email email;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return ListTile(
      onTap: onTap,
      leading: email.suspicious
          ? Icon(Icons.gpp_maybe, color: scheme.error)
          : Icon(
              email.needsDecision
                  ? Icons.mark_email_unread
                  : Icons.mark_email_read,
              color: scheme.primary,
            ),
      title: Text(
        email.subject.isEmpty ? '(без темы)' : email.subject,
        maxLines: 1,
        overflow: TextOverflow.ellipsis,
      ),
      subtitle: Text(
        '${email.from}\n${statusLabels[email.status] ?? email.status}',
        maxLines: 2,
      ),
      isThreeLine: true,
      trailing: email.urgency == 'высокая'
          ? Icon(Icons.priority_high, color: scheme.error)
          : null,
    );
  }
}
