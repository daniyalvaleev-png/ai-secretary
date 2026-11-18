/// Письмо — ровно то, что отдаёт GET /api/emails/{uid}
class Email {
  Email({
    required this.uid,
    required this.from,
    required this.subject,
    required this.body,
    required this.receivedAt,
    required this.urgency,
    required this.suggestedAction,
    required this.status,
    required this.draft,
    required this.suspicious,
    required this.suspicionReason,
    required this.meetingTitle,
    required this.meetingStart,
    required this.meetingEnd,
    required this.calendarNote,
    required this.calendarEventId,
  });

  factory Email.fromJson(Map<String, dynamic> j) => Email(
    uid: j['uid'] as int,
    from: j['from'] as String? ?? '',
    subject: j['subject'] as String? ?? '',
    body: j['body'] as String? ?? '',
    receivedAt: DateTime.tryParse(j['received_at'] as String? ?? '')?.toLocal(),
    urgency: j['urgency'] as String? ?? '',
    suggestedAction: j['suggested_action'] as String? ?? '',
    status: j['status'] as String? ?? '',
    draft: j['draft'] as String? ?? '',
    suspicious: j['suspicious'] as bool? ?? false,
    suspicionReason: j['suspicion_reason'] as String? ?? '',
    meetingTitle: j['meeting_title'] as String? ?? '',
    meetingStart: j['meeting_start'] as String? ?? '',
    meetingEnd: j['meeting_end'] as String? ?? '',
    calendarNote: j['calendar_note'] as String? ?? '',
    calendarEventId: j['calendar_event_id'] as String? ?? '',
  );

  final int uid;
  final String from;
  final String subject;
  final String body;
  final DateTime? receivedAt;
  final String urgency;
  final String suggestedAction;
  final String status;
  final String draft;
  final bool suspicious;
  final String suspicionReason;
  final String meetingTitle;
  final String meetingStart; // «2026-11-17 15:00», пусто — встречи нет
  final String meetingEnd;
  final String calendarNote;
  final String calendarEventId;

  /// Ход за владельцем: письмо ждёт решения или черновик ждёт отправки
  bool get needsDecision =>
      const {'pending', 'draft_ready', 'draft_failed'}.contains(status);

  /// Сервер сейчас что-то делает — экран письма сам перечитывает его, пока статус не сменится
  bool get busy => status == 'drafting' || status == 'sending';

  bool get canAddToCalendar =>
      meetingStart.isNotEmpty && calendarEventId.isEmpty;
}

/// Событие календаря из GET /api/calendar
class CalendarEvent {
  CalendarEvent({
    required this.title,
    required this.start,
    required this.end,
    required this.weekday,
  });

  factory CalendarEvent.fromJson(Map<String, dynamic> j) => CalendarEvent(
    title: j['title'] as String? ?? '',
    start: j['start'] as String? ?? '',
    end: j['end'] as String? ?? '',
    weekday: j['weekday'] as String? ?? '',
  );

  final String title;
  final String start; // «2026-11-17 15:00»
  final String end;
  final String weekday;
}

/// Событие из ленты GET /api/events: «пришло письмо», «черновик готов», «отправлено»…
class AppEvent {
  AppEvent({
    required this.id,
    required this.type,
    required this.uid,
    this.from = '',
    this.subject = '',
    this.status = '',
    this.text = '',
    this.important = false,
  });

  /// Шлюз перезапускался, часть событий могла потеряться — надо просто перечитать список
  AppEvent.resync() : this(id: 0, type: 'resync', uid: 0);

  factory AppEvent.fromJson(Map<String, dynamic> j) => AppEvent(
    id: (j['id'] as num).toInt(),
    type: j['type'] as String? ?? '',
    uid: (j['uid'] as num? ?? 0).toInt(),
    from: j['from'] as String? ?? '',
    subject: j['subject'] as String? ?? '',
    status: j['status'] as String? ?? '',
    text: j['text'] as String? ?? '',
    important: j['important'] as bool? ?? false,
  );

  final int id;
  final String type; // new_email, status, draft_ready, draft_failed, sent, send_failed, calendar_added, calendar_failed
  final int uid;
  final String from;
  final String subject;
  final String status;
  final String text; // готовая фраза для уведомления; пусто — показывать нечего
  final bool important; // стоит показать системное уведомление
}
