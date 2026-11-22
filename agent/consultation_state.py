"""Evidence-backed conversation state. No generated facts or product advice."""
import re

FIELDS = ('object', 'task', 'parameters', 'constraints', 'notes')
LABELS = {'объект': 'object', 'задача': 'task', 'известные сведения': 'parameters', 'условия': 'constraints'}
TYPED = re.compile(r'^(?:(Уточнение|Дополнение)\s*[—–-]\s*(объект|задача|известные сведения|условия)|Дополнение к задаче):[ \t]*', re.I | re.M)
OBJECT = r'(?:склад\w*|офис\w*|частн\w*\s+дом\w*|таунхаус\w*|магазин\w*|кафе\b|кофейн\w*|ресторан\w*|пекарн\w*|завод\w*|предприяти\w*|снт|производств\w*|торгов\w*\s+центр\w*|гостиниц\w*|жил\w*\s+дом\w*)'
META_REQUEST = re.compile(r'^(?:составь\w*|сформулируй\w*|дай\w*|дайте|покажи\w*|объясни\w*|расскажи\w*|повтори\w*|какие\s+шаги|что\s+дальше)', re.I)
NON_ASSERTION = re.compile(r'^(?:если|например|представим|допустим|модель сказала|консультант сказал)\b', re.I)


def norm(text):
    return ' '.join(str(text).casefold().replace('ё', 'е').split())


def typed_blocks(text):
    matches = list(TYPED.finditer(text))
    blocks = []
    for i, match in enumerate(matches):
        value = text[match.end():matches[i+1].start() if i+1 < len(matches) else len(text)].strip()
        if value:
            blocks.append((LABELS.get((match[2] or '').lower(), 'notes'), value, (match[1] or '').lower() == 'уточнение'))
    return (text[:matches[0].start()] if matches else text), blocks


def clauses(text):
    # Preserve quoted evidence, including decimal numbers, units and negation.
    result=[]
    for sentence in re.split(r'\n+|(?<=[.!?])\s+|;\s*', text):
        parts=[sentence] if NON_ASSERTION.search(sentence.strip()) else re.split(
            r',\s*(?=(?:площадь|\d|один|два|две|три|четыре|показания|расходы|режим|есть|нет|видим)\b)',sentence,flags=re.I)
        result.extend(part.strip(' \t\r\n;,.') for part in parts if part.strip(' \t\r\n;,.'))
    return result


def slot(text):
    t = norm(text)
    if re.search(r'площад|\bм[²2]\b', t): return 'area'
    if re.search(r'арендатор', t) and re.search(r'\d|один|два|две|три|четыре', t) and not re.search(r'счетчик|учет', t): return 'tenants'
    if re.search(r'снима|собира|сбор|переда|получ', t) and re.search(r'показан|данн', t): return 'readings'
    if re.search(r'отдельн|общий|общую|раздельн', t) and re.search(r'счет|учет|сумм', t): return 'meter_layout'
    if re.search(r'счетчик|прибор\w*\s+учета', t): return 'meters'
    if re.search(r'режим|график|смен[аыу]|круглосуточ', t): return 'schedule'
    if re.search(r'оборудован', t): return 'equipment'
    if re.search(r'вырос|рост|увеличил', t) and re.search(r'счет|расход|потреблен', t): return 'growth'
    if re.search(r'руб|₽|\bквт|расход\w*\s+(?:сейчас|теперь|состав)', t): return 'cost_consumption'
    if re.search(r'доступн|сохранил|под рукой', t) and re.search(r'счет|показан|данн', t): return 'available_data'
    return ''


def classify(text):
    t = norm(text)
    if '?' in text or META_REQUEST.search(text) or re.match(r'(?:если|например|представим|допустим|модель сказала|консультант сказал)\b', t): return None
    if re.match(r'(?:хочу|хотим|нужно|нужен|нужна|нужны|цель|задача)\b', t):
        return 'constraints' if re.search(r'подешевле|дешевле|без\s+замены|не\s+хочу|бюджет', t) else 'task'
    if re.match(r'(?:без\b|не\s+хотим|не\s+хочу|бюджет|ограничение)', t): return 'constraints'
    if slot(text): return 'parameters'
    if re.search(OBJECT, t) and re.match(r'(?:у нас|у меня|наш\w*|объект|это|теперь|вместо|не\s+|'+OBJECT+r')', t): return 'object'
    return None


def valid_entry(entry, messages):
    if not isinstance(entry, dict): return False
    i, value = entry.get('message_index'), entry.get('quote', entry.get('value'))
    return (type(i) is int and 0 <= i < len(messages) and messages[i].role == 'user'
            and isinstance(value, str) and len(value.strip()) >= 2 and norm(value) in norm(messages[i].content))


def build_facts(messages, previous=None, proposed=None):
    """Merge sparse snapshots; explicit edits/corrections replace older values.

    Every result is a literal user quote. Deterministic splitting handles common
    object/goal/measurement clauses; unfamiliar evidenced facts can still come
    from the model. Questions and requests to format a reply are not client goals.
    """
    events = []
    def add(field, value, index, priority=1, replace=False):
        events.append((index, priority, field, value.strip(), replace))
    for priority, source in enumerate((previous or {}, proposed or {})):
        for field in FIELDS:
            for item in source.get(field, []) if isinstance(source.get(field), list) else []:
                if not valid_entry(item, messages): continue
                index = item['message_index']
                value = item.get('quote', item.get('value'))
                contexts=[s.strip() for s in re.split(r'\n+|(?<=[.!?])\s+',messages[index].content) if norm(value) in norm(s)]
                if contexts and all(NON_ASSERTION.search(s) for s in contexts): continue
                plain, typed = typed_blocks(value)
                owners = typed_blocks(messages[index].content)[1]
                if typed: continue  # Reconstruct these from their authoritative UI labels below.
                for part in clauses(plain):
                    owner = next((key for key, quote, _ in owners if norm(part) in norm(quote)), None)
                    category = owner or classify(part)
                    if category is None:
                        if '?' in part or META_REQUEST.search(part) or re.match(r'(?:если|например|представим|допустим|модель сказала|консультант сказал)\b', norm(part)): continue
                        category = field
                    add(category, part, index, priority)
    for index, message in enumerate(messages):
        if message.role != 'user': continue
        plain, typed = typed_blocks(message.content)
        for part in clauses(plain):
            field = classify(part)
            if field: add(field, part, index, 2)
        for field, value, replace in typed:
            parts = [value] if field in ('object', 'task') else clauses(value)
            for part_index, part in enumerate(parts):
                add(field, part, index, 3, (replace and part_index == 0) or field in ('object', 'task'))
    state = {field: [] for field in FIELDS}
    for index, priority, field, value, replace in sorted(events, key=lambda event: (event[0], event[1])):
        if not value: continue
        # Category labels are already removed; never replace a goal with “make a plan”.
        if field == 'task' and META_REQUEST.search(value): continue
        items = state[field]
        if replace or field in ('object', 'task'):
            items = []
        property_key = slot(value) if field == 'parameters' else ''
        # New data can refer to a different period or meter. Do not silently
        # discard it just because both facts mention cost, area or readings.
        correction = re.search(r'\b(?:теперь|исправляю|исправление|точнее|ошибся|ошиблась)\b', norm(value))
        if property_key and correction:
            items = [item for item in items if not (slot(item['value']) == property_key and item['message_index'] < index)]
        items = [item for item in items if norm(item['value']) != norm(value)]
        items.append({'value': value, 'message_index': index})
        state[field] = items
    return state


def known_topics(facts):
    values = [entry['value'] for entries in facts.values() if isinstance(entries, list) for entry in entries if isinstance(entry, dict) and isinstance(entry.get('value'), str)]
    topics = {slot(value) for value in values} - {''}
    text = norm(' '.join(values))
    if 'readings' in topics and re.search(r'вручную|автомат|удаленно|по\s+gsm|через', text): topics.add('reading_method')
    if re.search(r'раз\s+в|ежемесяч|ежеднев|еженедель', text): topics.add('reading_frequency')
    if 'meter_layout' in topics or 'meters' in topics: topics.add('meter_presence')
    if any(re.search(OBJECT,norm(entry.get('value',''))) for entry in facts.get('object',[]) if isinstance(entry,dict)):
        topics.add('object_type')
    return topics


def answered_topics(question, facts):
    """Narrow topic checks, not keyword-based deletion of arbitrary advice."""
    q = norm(question)
    known = known_topics(facts)
    patterns = {
        'reading_method': r'(?:как(?:ова|ую|ой|им)?|способ|схем\w*|уточн\w*).*(?:снима\w*|собира\w*|сбор\w*|переда\w*)[^?]*(?:показан|данн)|(?:ручной|автоматический)\s+учет\?',
        'reading_frequency': r'как\s+часто|(?:какая|уточните)\s+периодичност',
        'meter_presence': r'(?:есть\s+ли|имеются\s+ли|у\s+вас\s+есть|наличие).*(?:счетчик|прибор\w*\s+учета)',
        'meter_layout': r'(?:есть\s+ли|имеются\s+ли).*(?:отдельн|общий).*счетчик|(?:только\s+общая|по\s+отдельным\s+зонам).*\?',
        'schedule': r'(?:менялся\s+ли|изменялся\s+ли|меняли\s+ли).*(?:режим|график)',
        'equipment': r'(?:менялся\s+ли|изменялся\s+ли|добавляли\s+ли|устанавливал\w*\s+ли|меняли\s+ли).*(?:оборудован)',
        'object_type': r'(?:какой|каков|уточн\w*|сообщ\w*).*(?:тип|вид)\s+(?:ваш\w*\s+)?(?:объект|организац|предприят)|(?:что\s+за|о\s+каком)\s+объект',
    }
    # Knowing the physical object does not establish the client's ownership role.
    if re.search(r'собственник|управляющ|арендатор|владел|\bроль',q): known.discard('object_type')
    return [key for key, pattern in patterns.items() if key in known and re.search(pattern, q)]


def without_answered_questions(answer, facts):
    """Omit redundant request sentences; never manufacture an alternative reply."""
    sentences = re.split(r'(?<=[.!?])\s+|\n+', answer)
    kept = [s for s in sentences if not (('?' in s or re.search(r'нужно уточнить|уточните|сообщите', s, re.I))
                                       and answered_topics(s, facts))]
    return '\n'.join(kept).strip(), len(sentences) - len(kept)


ACTION = re.compile(r'(?i)^(?:(?:сначала|затем|далее|также|для начала)\s*[,—:]?\s*)?(?:можно\s+)?(?:начн\w*|сравн\w*|сопостав\w*|провер\w*|собер\w*|собрат\w*|уточн\w*|выясн\w*|обсуд\w*|запиш\w*|посмотр\w*|подготов\w*|измер\w*|определ\w*|свяж\w*|обрат\w*|учт\w*|попрос\w*|проанализ\w*|рассмотр\w*|оцен\w*|провед\w*|провест\w*|зафикс\w*|налад\w*|внедр\w*)\b')


def extract_actions(answer):
    # Inline numbering is common in real model responses, as are infinitives.
    parts = re.split(r'\n+|(?=\b\d{1,2}[.)]\s)|(?<=[.!?])\s+', answer)
    result = []
    for part in parts:
        text = re.sub(r'^\s*(?:\d{1,2}[.)]|[-*•])\s*', '', part).strip(' \t\r\n;').replace('**', '')
        # A direct question can be punctuated with a full stop by the model.
        # Keep real investigation steps such as “Уточните у энергетика тариф”.
        if re.match(r'^(?:уточните|сообщите|скажите|подскажите)\s*[,—:]?\s*(?:есть\s+ли\s+у\s+вас|имеются\s+ли\s+у\s+вас|как\s+вы|(?:како[йев]|какая|сколько)\s+у\s+вас)\b', text, re.I):
            continue
        conditional_action = re.match(r'^если\b', text, re.I) and any(ACTION.search(clause.strip()) for clause in text.split(',')[1:])
        if 8 <= len(text) <= 650 and '?' not in text and (ACTION.search(text) or conditional_action) and text not in result:
            result.append(text)
    return result[:8]


def asks_for_plan(text):
    return bool(re.search(r'(?:план|конкретн\w*\s+шаг|(?:следующ|перв)\w*\s+действ|какие\s+(?:конкретные\s+)?шаг)', norm(text)))
