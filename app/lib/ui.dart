import 'package:material_ui/material_ui.dart';

/// Общий вид приложения: тема, цвета статусов, аватарки и даты по-русски.
/// Всё, что повторяется на разных экранах, живёт здесь.

const _seed = Color(0xFF4F46E5); // индиго — основной цвет

ThemeData buildTheme(Brightness brightness) {
  final light = brightness == Brightness.light;
  final scheme = ColorScheme.fromSeed(seedColor: _seed, brightness: brightness);
  final radius = BorderRadius.circular(14);

  return ThemeData(
    colorScheme: scheme,
    // Светло-серый фон, а карточки белые — так они «отрываются» от фона без теней
    scaffoldBackgroundColor: light ? const Color(0xFFF5F5FA) : scheme.surface,
    appBarTheme: AppBarTheme(
      backgroundColor: light ? const Color(0xFFF5F5FA) : scheme.surface,
      scrolledUnderElevation: 0,
      centerTitle: false,
      titleTextStyle: TextStyle(
        fontSize: 22,
        fontWeight: FontWeight.w700,
        color: scheme.onSurface,
      ),
    ),
    cardTheme: CardThemeData(
      elevation: 0,
      margin: EdgeInsets.zero,
      color: light ? Colors.white : scheme.surfaceContainer,
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(18),
        side: BorderSide(color: scheme.outlineVariant.withValues(alpha: 0.4)),
      ),
    ),
    inputDecorationTheme: InputDecorationTheme(
      filled: true,
      fillColor: light
          ? scheme.surfaceContainerHighest.withValues(alpha: 0.5)
          : scheme.surfaceContainerHigh,
      border: OutlineInputBorder(
        borderRadius: radius,
        borderSide: BorderSide.none,
      ),
      focusedBorder: OutlineInputBorder(
        borderRadius: radius,
        borderSide: BorderSide(color: scheme.primary, width: 1.5),
      ),
    ),
    filledButtonTheme: FilledButtonThemeData(
      style: FilledButton.styleFrom(
        minimumSize: const Size(0, 48),
        padding: const EdgeInsets.symmetric(horizontal: 16),
        shape: RoundedRectangleBorder(borderRadius: radius),
      ),
    ),
    outlinedButtonTheme: OutlinedButtonThemeData(
      style: OutlinedButton.styleFrom(
        minimumSize: const Size(0, 48),
        padding: const EdgeInsets.symmetric(horizontal: 16),
        shape: RoundedRectangleBorder(borderRadius: radius),
      ),
    ),
    textButtonTheme: TextButtonThemeData(
      style: TextButton.styleFrom(minimumSize: const Size(0, 48)),
    ),
    snackBarTheme: SnackBarThemeData(
      behavior: SnackBarBehavior.floating,
      shape: RoundedRectangleBorder(borderRadius: radius),
    ),
    dividerTheme: DividerThemeData(
      color: scheme.outlineVariant.withValues(alpha: 0.4),
    ),
  );
}

// --- Статусы ---

const _statusLabels = {
  'pending': 'Ждёт решения',
  'drafting': 'Пишу черновик…',
  'draft_ready': 'Черновик готов',
  'draft_failed': 'Черновик не вышел',
  'sending': 'Отправляю…',
  'sent': 'Отправлено',
  'rejected': 'Отклонено',
  'skipped': 'Без ответа',
  'confirmed': 'Подтверждено',
  'done': 'Готово',
  'approved': 'Одобрено',
};

String statusLabel(String status) => _statusLabels[status] ?? status;

/// Цвет «таблетки» статуса: жёлтый — ждёт тебя, синий — готово к отправке,
/// красный — ошибка, зелёный — отправлено, серый — закрыто
(Color bg, Color fg) statusColors(BuildContext context, String status) {
  final s = Theme.of(context).colorScheme;
  final light = Theme.of(context).brightness == Brightness.light;
  return switch (status) {
    'pending' =>
      light
          ? (const Color(0xFFFFF1CC), const Color(0xFF7A4F00))
          : (const Color(0xFF3D2E00), const Color(0xFFFFD27A)),
    'draft_ready' => (s.primaryContainer, s.onPrimaryContainer),
    'draft_failed' => (s.errorContainer, s.onErrorContainer),
    'drafting' || 'sending' => (s.secondaryContainer, s.onSecondaryContainer),
    'sent' || 'done' || 'approved' || 'confirmed' =>
      light
          ? (const Color(0xFFDDF5E3), const Color(0xFF1E6B37))
          : (const Color(0xFF143D22), const Color(0xFF8FE0A8)),
    _ => (s.surfaceContainerHighest, s.onSurfaceVariant),
  };
}

class StatusPill extends StatelessWidget {
  const StatusPill(this.status, {super.key});

  final String status;

  @override
  Widget build(BuildContext context) {
    final (bg, fg) = statusColors(context, status);
    final busy = status == 'drafting' || status == 'sending';
    return Pill(
      label: statusLabel(status),
      bg: bg,
      fg: fg,
      leading: busy
          ? SizedBox(
              width: 10,
              height: 10,
              child: CircularProgressIndicator(strokeWidth: 1.5, color: fg),
            )
          : null,
    );
  }
}

/// Маленькая плашка с текстом: статус, «Срочно» и т.п.
class Pill extends StatelessWidget {
  const Pill({
    super.key,
    required this.label,
    required this.bg,
    required this.fg,
    this.leading,
  });

  final String label;
  final Color bg;
  final Color fg;
  final Widget? leading;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 4),
      decoration: BoxDecoration(
        color: bg,
        borderRadius: BorderRadius.circular(99),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (leading != null) ...[leading!, const SizedBox(width: 6)],
          Text(
            label,
            style: TextStyle(
              color: fg,
              fontSize: 12,
              fontWeight: FontWeight.w600,
            ),
          ),
        ],
      ),
    );
  }
}

class UrgentPill extends StatelessWidget {
  const UrgentPill({super.key});

  @override
  Widget build(BuildContext context) {
    final s = Theme.of(context).colorScheme;
    return Pill(
      label: 'Срочно',
      bg: s.errorContainer,
      fg: s.onErrorContainer,
      leading: Icon(Icons.bolt, size: 12, color: s.onErrorContainer),
    );
  }
}

// --- Отправитель ---

/// `Иван Петров <ivan@mail.ru>` → «Иван Петров»; `ivan@mail.ru` → «ivan»
String senderName(String from) {
  final f = from.trim();
  final lt = f.indexOf('<');
  if (lt > 0) {
    final name = f.substring(0, lt).trim().replaceAll('"', '');
    if (name.isNotEmpty) return name;
  }
  final addr = senderAddress(f);
  final at = addr.indexOf('@');
  return at > 0 ? addr.substring(0, at) : addr;
}

String senderAddress(String from) {
  final m = RegExp(r'<([^>]+)>').firstMatch(from);
  return (m?[1] ?? from).trim();
}

const _avatarHues = [
  Color(0xFF4F46E5),
  Color(0xFF0891B2),
  Color(0xFF059669),
  Color(0xFFD97706),
  Color(0xFFDB2777),
  Color(0xFF7C3AED),
  Color(0xFF2563EB),
  Color(0xFF65A30D),
];

/// Кружок с первой буквой отправителя; цвет постоянный для одного адреса
class SenderAvatar extends StatelessWidget {
  const SenderAvatar({
    super.key,
    required this.from,
    this.suspicious = false,
    this.size = 44,
  });

  final String from;
  final bool suspicious;
  final double size;

  @override
  Widget build(BuildContext context) {
    final s = Theme.of(context).colorScheme;
    if (suspicious) {
      return CircleAvatar(
        radius: size / 2,
        backgroundColor: s.errorContainer,
        child: Icon(Icons.gpp_maybe, color: s.onErrorContainer),
      );
    }
    final addr = senderAddress(from).toLowerCase();
    final hue =
        _avatarHues[addr.codeUnits.fold(0, (a, b) => a + b) %
            _avatarHues.length];
    final name = senderName(from);
    return CircleAvatar(
      radius: size / 2,
      backgroundColor: hue.withValues(alpha: 0.15),
      child: Text(
        name.isEmpty ? '?' : name.characters.first.toUpperCase(),
        style: TextStyle(
          color: hue,
          fontWeight: FontWeight.w700,
          fontSize: size * 0.4,
        ),
      ),
    );
  }
}

// --- Даты по-русски (без лишних пакетов) ---

const _monthsShort = [
  'янв',
  'февр',
  'марта',
  'апр',
  'мая',
  'июня',
  'июля',
  'авг',
  'сент',
  'окт',
  'нояб',
  'дек',
];
const _months = [
  'января',
  'февраля',
  'марта',
  'апреля',
  'мая',
  'июня',
  'июля',
  'августа',
  'сентября',
  'октября',
  'ноября',
  'декабря',
];
const _weekdays = [
  'Понедельник',
  'Вторник',
  'Среда',
  'Четверг',
  'Пятница',
  'Суббота',
  'Воскресенье',
];

String _two(int n) => n.toString().padLeft(2, '0');
String hhmm(DateTime d) => '${_two(d.hour)}:${_two(d.minute)}';

DateTime _day(DateTime d) => DateTime(d.year, d.month, d.day);

/// Для списка писем: «сейчас», «12 мин», «14:05», «вчера», «17 нояб», «17.11.2025»
String shortDate(DateTime? d, {DateTime? now}) {
  if (d == null) return '';
  now ??= DateTime.now();
  final diff = now.difference(d);
  if (diff.inMinutes < 1 && !diff.isNegative) return 'сейчас';
  if (diff.inMinutes < 60 && !diff.isNegative) return '${diff.inMinutes} мин';
  final days = _day(now).difference(_day(d)).inDays;
  if (days == 0) return hhmm(d);
  if (days == 1) return 'вчера';
  if (d.year == now.year) return '${d.day} ${_monthsShort[d.month - 1]}';
  return '${_two(d.day)}.${_two(d.month)}.${d.year}';
}

/// Для экрана письма: «сегодня в 14:05», «17 ноября в 09:30»
String fullDate(DateTime? d, {DateTime? now}) {
  if (d == null) return '';
  now ??= DateTime.now();
  final days = _day(now).difference(_day(d)).inDays;
  final day = switch (days) {
    0 => 'сегодня',
    1 => 'вчера',
    _ =>
      '${d.day} ${_months[d.month - 1]}${d.year == now.year ? '' : ' ${d.year}'}',
  };
  return '$day в ${hhmm(d)}';
}

/// Заголовок дня в календаре: «Сегодня», «Завтра», «Пятница, 20 ноября»
String dayTitle(DateTime d, {DateTime? now}) {
  now ??= DateTime.now();
  final days = _day(d).difference(_day(now)).inDays;
  final date = '${d.day} ${_months[d.month - 1]}';
  return switch (days) {
    0 => 'Сегодня, $date',
    1 => 'Завтра, $date',
    _ => '${_weekdays[d.weekday - 1]}, $date',
  };
}

/// «2026-11-17 15:00» (так время отдаёт шлюз) → DateTime
DateTime? parseLocal(String s) =>
    s.isEmpty ? null : DateTime.tryParse(s.replaceFirst(' ', 'T'));

// --- Общие кусочки экранов ---

/// Карточка-секция с заголовком и иконкой
class SectionCard extends StatelessWidget {
  const SectionCard({
    super.key,
    required this.icon,
    required this.title,
    required this.child,
    this.color,
    this.trailing,
  });

  final IconData icon;
  final String title;
  final Widget child;
  final Color? color;
  final Widget? trailing;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final c = color ?? theme.colorScheme.primary;
    return Card(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 14, 16, 16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(icon, size: 18, color: c),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    title,
                    style: theme.textTheme.labelLarge?.copyWith(
                      color: c,
                      fontWeight: FontWeight.w700,
                    ),
                  ),
                ),
                ?trailing,
              ],
            ),
            const SizedBox(height: 10),
            child,
          ],
        ),
      ),
    );
  }
}

/// Большая иконка + текст, когда показывать нечего
class EmptyState extends StatelessWidget {
  const EmptyState({
    super.key,
    required this.icon,
    required this.title,
    this.subtitle,
    this.action,
  });

  final IconData icon;
  final String title;
  final String? subtitle;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 32, vertical: 48),
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          Container(
            width: 88,
            height: 88,
            decoration: BoxDecoration(
              color: theme.colorScheme.primaryContainer.withValues(alpha: 0.6),
              shape: BoxShape.circle,
            ),
            child: Icon(icon, size: 40, color: theme.colorScheme.primary),
          ),
          const SizedBox(height: 20),
          Text(
            title,
            textAlign: TextAlign.center,
            style: theme.textTheme.titleMedium?.copyWith(
              fontWeight: FontWeight.w600,
            ),
          ),
          if (subtitle != null) ...[
            const SizedBox(height: 6),
            Text(
              subtitle!,
              textAlign: TextAlign.center,
              style: theme.textTheme.bodyMedium?.copyWith(
                color: theme.colorScheme.onSurfaceVariant,
              ),
            ),
          ],
          if (action != null) ...[const SizedBox(height: 16), action!],
        ],
      ),
    );
  }
}
