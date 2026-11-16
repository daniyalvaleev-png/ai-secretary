import 'package:material_ui/material_ui.dart';

import '../api.dart';
import '../models.dart';

/// События на неделю вперёд, сгруппированные по дням
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
      if (mounted) setState(() => _events = events);
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final events = _events;
    Widget body;
    if (events == null) {
      body = Center(
        child: _error != null
            ? Text(_error!)
            : const CircularProgressIndicator(),
      );
    } else if (events.isEmpty) {
      body = const Center(child: Text('На неделю вперёд событий нет'));
    } else {
      final children = <Widget>[];
      String? day;
      for (final e in events) {
        final d = e.start.length >= 10 ? e.start.substring(0, 10) : e.start;
        if (d != day) {
          day = d;
          children.add(
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 16, 16, 4),
              child: Text(
                '${e.weekday}, $d',
                style: Theme.of(context).textTheme.titleSmall,
              ),
            ),
          );
        }
        children.add(
          ListTile(
            leading: const Icon(Icons.event),
            title: Text(e.title),
            subtitle: Text('${_time(e.start)}–${_time(e.end)}'),
          ),
        );
      }
      body = ListView(children: children);
    }

    return Scaffold(
      appBar: AppBar(
        title: const Text('Календарь'),
        actions: [
          IconButton(onPressed: _load, icon: const Icon(Icons.refresh)),
        ],
      ),
      body: body,
    );
  }

  static String _time(String s) => s.length >= 16 ? s.substring(11, 16) : s;
}
