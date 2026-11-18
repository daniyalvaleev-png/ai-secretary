import 'package:material_ui/material_ui.dart';

import '../api.dart';
import '../models.dart';
import '../ui.dart';

/// События на неделю вперёд, по дням: «Сегодня», «Завтра», «Пятница, 20 ноября»
class CalendarScreen extends StatefulWidget {
  const CalendarScreen({super.key, required this.api});

  final ApiClient api;

  @override
  State<CalendarScreen> createState() => _CalendarScreenState();
}

class _CalendarScreenState extends State<CalendarScreen> {
  List<CalendarEvent>? _events;
  String? _error;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    try {
      final events = await widget.api.calendar();
      if (mounted) {
        setState(() {
          _events = events;
          _error = null;
        });
      }
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final events = _events;
    final Widget body;
    if (events == null) {
      body = Center(
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
      );
    } else if (events.isEmpty) {
      body = RefreshIndicator(
        onRefresh: _load,
        child: ListView(
          children: const [
            EmptyState(
              icon: Icons.event_available,
              title: 'На неделю вперёд событий нет',
              subtitle: 'Неделя свободна — можно соглашаться на встречи',
            ),
          ],
        ),
      );
    } else {
      body = RefreshIndicator(
        onRefresh: _load,
        child: ListView(
          padding: const EdgeInsets.fromLTRB(16, 0, 16, 24),
          children: _byDay(events),
        ),
      );
    }

    return Scaffold(
      appBar: AppBar(
        title: const Text('Календарь'),
        actions: [
          IconButton(
            tooltip: 'Обновить',
            onPressed: _load,
            icon: const Icon(Icons.refresh),
          ),
          const SizedBox(width: 4),
        ],
      ),
      body: body,
    );
  }

  List<Widget> _byDay(List<CalendarEvent> events) {
    final theme = Theme.of(context);
    final children = <Widget>[];
    String? day;
    for (final e in events) {
      final start = parseLocal(e.start);
      final d = e.start.length >= 10 ? e.start.substring(0, 10) : e.start;
      if (d != day) {
        day = d;
        children.add(
          Padding(
            padding: const EdgeInsets.fromLTRB(4, 16, 4, 8),
            child: Text(
              start != null ? dayTitle(start) : '${e.weekday}, $d',
              style: theme.textTheme.titleSmall?.copyWith(
                fontWeight: FontWeight.w700,
                color: theme.colorScheme.primary,
              ),
            ),
          ),
        );
      } else {
        children.add(const SizedBox(height: 8));
      }
      children.add(_EventCard(event: e));
    }
    return children;
  }
}

class _EventCard extends StatelessWidget {
  const _EventCard({required this.event});

  final CalendarEvent event;

  static String _time(String s) => s.length >= 16 ? s.substring(11, 16) : s;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final s = theme.colorScheme;
    return Card(
      child: IntrinsicHeight(
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 14, 12, 14),
              child: Column(
                mainAxisAlignment: MainAxisAlignment.center,
                children: [
                  Text(
                    _time(event.start),
                    style: theme.textTheme.titleSmall?.copyWith(
                      fontWeight: FontWeight.w700,
                    ),
                  ),
                  Text(
                    _time(event.end),
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: s.onSurfaceVariant,
                    ),
                  ),
                ],
              ),
            ),
            Container(
              width: 4,
              margin: const EdgeInsets.symmetric(vertical: 12),
              decoration: BoxDecoration(
                color: s.primary,
                borderRadius: BorderRadius.circular(2),
              ),
            ),
            Expanded(
              child: Padding(
                padding: const EdgeInsets.all(14),
                child: Align(
                  alignment: Alignment.centerLeft,
                  child: Text(
                    event.title.isEmpty ? '(без названия)' : event.title,
                    style: theme.textTheme.titleMedium,
                  ),
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}
