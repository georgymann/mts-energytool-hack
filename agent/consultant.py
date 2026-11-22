"""Stateless consultation service used by both CSV runner and backend integration."""
from dataclasses import asdict
import json
import re
import time

from contracts import ConsultationRequest, ConsultationResult, LeadState
from dialogue import retrieval_query
from llm_client import ModelError
from prompts import ANSWER_PROMPT, SYSTEM_PROMPT
from consultation_state import OBJECT, build_facts, answered_topics, extract_actions, asks_for_plan, without_answered_questions

TECHNICAL_ANSWER = ("Сейчас не удалось получить ответ модели из-за технического сбоя. "
                    "Это не означает, что информации нет в базе знаний. "
                    "Попробуйте повторить вопрос позже; заявку пока не отправляли.")
FIELDS = ("full_name", "phone", "email", "company")
CONTACT_FIELDS = ("phone", "email")
FIELD_LABELS = {"full_name": "ФИО", "phone": "телефон", "company": "название компании", "contact": "телефон или email"}


def sanitize_delivery(answer):
    sent_claim = re.search(r"(?:заявк\w*\s+(?:успешно\s+)?(?:отправлен|передан)|"
                           r"(?:отправил|передал)\w*\s+(?:ваш\w*\s+)?заявк)", answer, re.I)
    if sent_claim:
        answer = re.sub(r"[^.!?\n]*" + re.escape(sent_claim.group(0)) + r"[^.!?\n]*[.!?]?",
                        "Заявку можно подготовить; отправки менеджеру пока нет.", answer, flags=re.I)
    return answer.strip()


def normalized(text):
    return " ".join(text.casefold().replace("ё", "е").split())


def initial_cost_discovery(request, facts):
    """Only a first, generic savings request with no object or product in view."""
    users = [m.content for m in request.messages if m.role == 'user']
    if len(users) != 1 or facts.get('object'):
        return False
    text = normalized(users[0])
    if re.search(OBJECT,text) or re.search(r'\d|тариф|ценов\w*\s+категор|оптов|орэм|солнеч|сэс\b|платформ|подключ|совместим|сравн|стоимост|цен[аыу]',text):
        return False
    return bool(re.search(r'снизить|сократить|уменьшить|сэкономить|выросли\s+счет',text) and
                re.search(r'расход|счет|электроэнерг|платеж',text) and
                re.search(r'с\s+чего\s+начать|не\s+(?:знаю|понимаю)|что\s+делать',text))


def bill_diagnosis_focus(request, facts):
    """First investigative steps for a known object, not a loss-analysis project.

    Only the customer's words may establish interest in the specialised product;
    an earlier assistant suggestion must not turn it into a client requirement.
    """
    if not facts.get('object') or not asks_for_plan(request.messages[-1].content):
        return False
    customer_text=normalized(' '.join(m.content for m in request.messages if m.role=='user'))
    if re.search(r'потер|хищен|воров|неучтен|аномал',customer_text):
        return False
    return bool(re.search(r'(?:рост|вырос|увелич|скач|подорож).{0,65}(?:счет|платеж|расход|потребл)|'
                          r'(?:счет|платеж|расход|потребл).{0,65}(?:рост|вырос|увелич|скач|подорож)',customer_text))


def loss_product_fragment(fragment):
    """Use explicit document/section ownership, never isolated body keywords."""
    heading=normalized(fragment.text.split('\n',1)[0])
    return (fragment.document_id.endswith('24-losses.md') or
            bool(re.search(r'коммерческ\w*\s+потер|поиск\w*\s+(?:коммерческ\w*\s+)?потер',heading)))


def meter_reuse_question(request, fragments):
    """Route only questions about reusing the client's existing electricity meters.

    A named model passes this boundary only with explicit affirmative support in
    a retrieved sentence. Generic support for many meters is insufficient.
    """
    latest = normalized(request.messages[-1].content)
    if not (re.search(r'счетчик|прибор\w*\s+учета',latest) and
            re.search(r'существующ|имеющ|уже|эт(?:и|их|ими)\b|стар\w*|без\s+замен',latest) and
            re.search(r'можно|подключ|совместим|автоматизац|использ',latest) and
            ('?' in latest or re.search(r'можно\s+ли|подойдут\s+ли',latest))):
        return None
    model_unknown=bool(re.search(r'(?:тип|модел)\w*[^.!?]{0,65}(?:неизвест|не\s+зна)|'
                                 r'(?:не\s+знаю|не\s+знаем|не\s+помню)[^.!?]{0,65}(?:тип|модел)',latest))
    models = []
    pattern = r'\b(?:[А-ЯЁ][а-яё-]{2,30}\s+\d[\w.-]*|[A-Za-zА-ЯЁ][A-Za-zА-Яа-яЁё0-9_.-]*\d[A-Za-zА-Яа-яЁё0-9_.-]*)\b'
    for message in request.messages:
        if message.role != 'user': continue
        for match in re.finditer(pattern,message.content):
            nearby = normalized(message.content[max(0,match.start()-65):match.end()+35])
            if re.search(r'счетчик|прибор|модел|тип',nearby) and match.group() not in models:
                models.append(match.group())
    supported_models=set()
    for model in models:
        for fragment in fragments:
            for sentence in re.split(r'(?<=[.!?])\s+|\n+',fragment.text):
                text=normalized(sentence)
                if (normalized(model) in text and re.search(r'поддержива|совместим|можно\s+подключ|подключается',text)
                        and not re.search(r'\bне\b|нет\s+подтвержден|неизвест',text)):
                    supported_models.add(model)
    if models and not model_unknown and len(supported_models)==len(models): return None
    return {'models':models,'model_unknown':model_unknown}


def confused_wholesale_threshold(answer, fragments):
    """Detect only the observed transfer of the tariff-category 670 kW rule."""
    tariff_evidence = any('11-price-categories' in f.document_id and
                          re.search(r'670\s*квт',normalized(f.text)) for f in fragments)
    if not tariff_evidence: return False
    for sentence in re.split(r'(?<=[.!?])\s+|\n+',answer):
        text=normalized(sentence)
        if not (re.search(r'670\s*квт',text) and re.search(r'оптов|орэм',text)): continue
        if re.search(r'750\s*квт|недостаточ|нельзя|невозмож|не\s+(?:является|служит|означает|позволяет|дает|достаточно)|не\s+порог|не\s+для\s+оптов',text): continue
        if (re.search(r'(?:если|от|при|выше|превышает|не\s+менее).{0,70}670\s*квт.{0,120}(?:оптов|орэм)',text) or
                re.search(r'(?:оптов|орэм).{0,100}(?:порог|от|выше|достаточ).{0,30}670\s*квт',text)):
            return True
    return False


def repeats_previous_answer(answer, messages):
    """Reject stalled substantial replies, while allowing an explicit repeat request.

    This checks progression, not semantic correctness: short acknowledgements and
    newly worded answers still need the normal model/source checks.
    """
    if len(messages) < 3:
        return False
    latest = normalized(messages[-1].content)
    if re.search(r"\b(?:повтори(?:те|ть)?|напомни(?:те|ть)?|повтор\w*\s+(?:ответ|объяснен)|"
                 r"(?:скажи(?:те)?|объясни(?:те)?)\s+(?:это\s+)?(?:еще|ещё)\s+раз)\b", latest):
        return False
    assistant_index = next((i for i in range(len(messages) - 2, -1, -1)
                            if messages[i].role == "assistant"), None)
    if assistant_index is None:
        return False
    earlier_user = next((m.content for m in reversed(messages[:assistant_index])
                         if m.role == "user"), "")
    if latest == normalized(earlier_user):
        return False  # A deliberate resubmission of the same question may have the same answer.
    words = lambda text: re.findall(r"\w+", normalized(text))
    previous_words = words(messages[assistant_index].content)
    previous = messages[assistant_index].content
    substantial = len(previous_words) >= 12 or ("?" in previous and len(previous_words) >= 4)
    return substantial and words(answer) == previous_words


def phone_digits(value):
    digits = re.sub(r"\D", "", value)
    if len(digits) == 11 and digits.startswith("8"):
        digits = "7" + digits[1:]
    return digits


def user_evidence(item, messages):
    index, quote = item.get("message_index"), item.get("quote")
    if type(index) is not int or not 0 <= index < len(messages):
        return False
    message = messages[index]
    return (message.role == "user" and isinstance(quote, str) and len(quote.strip()) >= 2
            and normalized(quote) in normalized(message.content))


def valid_email(value):
    """One bounded ASCII mailbox; syntax only, no claim of deliverability."""
    if not isinstance(value, str) or len(value) > 254 or value.count("@") != 1:
        return False
    local, host = value.split("@")
    if (not 1 <= len(local) <= 64 or not re.fullmatch(r"[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+", local)
            or local.startswith(".") or local.endswith(".") or ".." in local):
        return False
    labels = host.split(".")
    return len(labels) >= 2 and all(re.fullmatch(r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?", label) for label in labels)


def contact_value(field, value, quote, message_text=None):
    if not isinstance(value, str) or not value.strip() or len(value) > (254 if field == "email" else 200):
        return None
    value = value.strip()
    if field == "phone":
        digits = phone_digits(value)
        candidates = re.findall(r"\+?\d[\d ()\-]{8,}\d", quote)
        if 10 <= len(digits) <= 15 and any(phone_digits(c) == digits for c in candidates):
            return "+" + digits if len(digits) > 10 else digits
        return None
    if field == "email":
        # Exact whole mailbox evidence prevents a substring of another address
        # from being accepted; retain the spelling supplied by the user.
        if not valid_email(value):
            return None
        pattern = r"(?<![\w.!#$%&'*+/=?^_`{|}~@-])[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+(?![\w.@-])"
        quoted = [c.rstrip(".") for c in re.findall(pattern, quote)
                  if c.rstrip(".").casefold() == value.casefold()]
        if not quoted:
            return None
        # A model quote may be a substring of a larger mailbox. Recheck the
        # original user message, then keep its spelling rather than model case.
        candidates = [c.rstrip(".") for c in re.findall(pattern, message_text if message_text is not None else quote)
                      if c.rstrip(".").casefold() == value.casefold()]
        return next((c for c in candidates if c in quoted), candidates[0] if candidates else None)
    if normalized(value) not in normalized(quote):
        return None
    return value


def seed_lead(request):
    lead = LeadState(request.session_id)
    old = request.lead
    if old:
        for field in FIELDS:
            evidence = old.evidence.get(field, {})
            if user_evidence(evidence, request.messages):
                value = contact_value(field, old.fields.get(field), evidence["quote"],
                                      request.messages[evidence["message_index"]].content)
                if value:
                    lead.fields[field] = value
                    lead.evidence[field] = dict(evidence)
                elif field not in old.fields and re.search(r"удал|забуд|не\s+использ|отзыва", evidence["quote"], re.I):
                    # Keep withdrawal evidence across turns so an older model extraction
                    # cannot resurrect a contact that the customer already removed.
                    lead.evidence[field] = dict(evidence)
        intent = old.evidence.get("intent", {})
        if user_evidence(intent, request.messages):
            lead.requested, lead.declined = old.requested, old.declined
            lead.evidence["intent"] = dict(intent)
    return lead


def extract_lead(payload, request):
    lead = seed_lead(request)
    for item in sorted(payload.get("contacts", []), key=lambda x: x.get("message_index", -1)
                       if type(x.get("message_index")) is int else -1):
        field = item.get("field")
        if field not in FIELDS or not user_evidence(item, request.messages):
            continue
        old_index = lead.evidence.get(field, {}).get("message_index", -1)
        if item["message_index"] < old_index:
            continue
        evidence = {"quote": item["quote"], "message_index": item["message_index"]}
        if item.get("value") is None:
            # Null is accepted only with an explicit withdrawal instruction.
            if re.search(r"удал|забуд|не\s+использ|отзыва", item["quote"], re.I):
                lead.fields.pop(field, None)
                lead.evidence[field] = evidence
            continue
        value = contact_value(field, item.get("value"), item["quote"],
                              request.messages[item["message_index"]].content)
        if value:
            lead.fields[field], lead.evidence[field] = value, evidence
    intent = payload.get("intent", {})
    if (user_evidence(intent, request.messages)
            and intent["message_index"] >= lead.evidence.get("intent", {}).get("message_index", -1)):
        if type(intent.get("requested")) is bool and type(intent.get("declined")) is bool:
            lead.requested = intent["requested"] and not intent["declined"]
            lead.declined = intent["declined"]
            lead.evidence["intent"] = {"quote": intent["quote"], "message_index": intent["message_index"]}
    return lead


def lead_is_ready(lead):
    # Organizer Q&A 14–15: one contact is enough; name/company are optional.
    return bool(lead.requested and not lead.declined and any(lead.fields.get(f) for f in CONTACT_FIELDS))


def parse_payload(raw):
    raw = re.sub(r"^```(?:json)?\s*|\s*```$", "", raw.strip(), flags=re.I)
    try:
        obj = json.loads(raw)
    except (ValueError, TypeError):
        raise ModelError("invalid_consultation_json", retryable=True) from None
    if not isinstance(obj, dict):
        raise ModelError("invalid_consultation_schema", retryable=True)
    if (not isinstance(obj.get("answer"), str) or not obj["answer"].strip()
            or len(obj["answer"]) > 10000
            or obj.get("status") not in ("answered", "clarification", "knowledge_gap", "out_of_scope")):
        raise ModelError("invalid_consultation_schema", retryable=True)
    for key in ("citations", "contacts"):
        if not isinstance(obj.get(key), list) or len(obj[key]) > 40 or not all(isinstance(x, dict) for x in obj[key]):
            raise ModelError("invalid_consultation_schema", retryable=True)
    if not isinstance(obj.get("summary"), dict) or not isinstance(obj.get("intent"), dict):
        raise ModelError("invalid_consultation_schema", retryable=True)
    return obj


def validate_sources(payload, fragments):
    by_id = {f.source_id: f for f in fragments}
    sources, seen = [], set()
    for citation in payload["citations"]:
        sid, quote = citation.get("source_id"), citation.get("quote")
        if not isinstance(sid, str) or sid not in by_id or not isinstance(quote, str):
            raise ModelError("ungrounded_citation", retryable=True)
        fragment = by_id[sid]
        if len(quote.strip()) < 12 or normalized(quote) not in normalized(fragment.text):
            raise ModelError("ungrounded_citation", retryable=True)
        if sid not in seen:
            sources.append({"document_id": fragment.document_id, "chunk_id": fragment.chunk_id,
                            "source_id": sid, "quote": quote})
            seen.add(sid)
    if payload["status"] == "answered" and not sources:
        raise ModelError("missing_evidence", retryable=True)
    return sources


def unconfirmed_integration_target(question, fragments):
    """An explicit named integration needs evidence about that exact target.

    This deliberately covers proper Latin identifiers and quoted names, not
    generic classes such as «с системой учёта» or names inferred by the model.
    Missing retrieval evidence means unconfirmed, never unsupported.
    """
    if not re.search(r'интегр|совместим', question, re.I):
        return None
    pattern = r'\b(?:с|со)\s+(?:системой|платформой|сервисом)\s+(?:[«"]([^»"\n]{2,80})[»"]|([A-Z][A-Za-z0-9_.-]{2,79}))'
    targets = [a or b for a,b in re.findall(pattern,question)]
    for target in targets:
        target=target.rstrip('.?')
        if not any(normalized(target) in normalized(f.text) for f in fragments):
            return target
    return None


def make_summary(payload, request, lead, fragments):
    raw = payload.get("summary", {})
    summary = build_facts(request.messages, request.summary_so_far, raw)
    summary["state_version"] = 2
    source_ids = {f.source_id: f for f in fragments}
    products = raw.get("products", [])
    summary["products"] = []
    if isinstance(products, list):
        for product in products[:10]:
            if not isinstance(product, dict):
                continue
            sid, name = product.get("source_id"), product.get("name")
            if isinstance(sid, str) and sid in source_ids and isinstance(name, str) and name.strip():
                if normalized(name) in normalized(source_ids[sid].text):
                    summary["products"].append({"name": name, "source_id": sid})
    questions = raw.get("manager_questions", [])
    summary["manager_questions"] = [q[:500] for q in questions[:10] if isinstance(q, str) and not answered_topics(q, summary)] if isinstance(questions, list) else []
    summary["action_plan"] = extract_actions(payload.get("answer", "")) if payload.get("status") in ('answered', 'clarification') else []
    summary["contacts"] = dict(lead.fields)
    summary["missing_fields"] = [] if any(lead.fields.get(f) for f in CONTACT_FIELDS) else ["contact"]
    summary["delivery_status"] = "prepared" if lead_is_ready(lead) else "draft"
    summary["sent"] = False
    return summary


class Consultant:
    def __init__(self, client, retriever, settings):
        self.client, self.retriever, self.settings = client, retriever, settings

    def respond(self, request: ConsultationRequest, *, deadline=None, answer_only=False):
        started = time.monotonic()
        # Request is revalidated in case a caller mutated it after construction.
        request.__post_init__()
        lead = seed_lead(request)
        confirmed_facts = build_facts(request.messages, request.summary_so_far) if not answer_only else {}
        bill_diagnosis = not answer_only and bill_diagnosis_focus(request,confirmed_facts)
        excluded_sources=[]
        fragments, used_chars, seen = [], 0, set()
        try:
            candidates = self.retriever.search(retrieval_query(request.messages), self.settings.top_k)
            for fragment in candidates:
                if fragment.source_id in seen or not fragment.text.strip():
                    continue
                if bill_diagnosis and loss_product_fragment(fragment):
                    excluded_sources.append(fragment.source_id)
                    continue
                if used_chars + len(fragment.text) > self.settings.context_chars:
                    continue  # Never cut a qualifier away from its section.
                fragments.append(fragment)
                seen.add(fragment.source_id)
                used_chars += len(fragment.text)
        except Exception:
            return self._failure(request, lead, started, "retrieval_error")
        context = {
            "messages": [{"index": i, **asdict(m)} for i, m in enumerate(request.messages)],
            "lead_so_far": asdict(lead),
            "sources": [{"source_id": f.source_id, "text": f.text} for f in fragments],
        }
        if answer_only:
            # CSV judges only answer text. Lead extraction and exact-quote schema
            # validation belong to the interactive backend contract, not this path.
            context.pop("lead_so_far")
            model_messages = [{"role": "system", "content": ANSWER_PROMPT},
                              {"role": "user", "content": json.dumps(context, ensure_ascii=False)}]
            try:
                answer = self.client.complete(model_messages, deadline=deadline).strip()
                # Tolerate a model wrapping an otherwise usable answer in JSON.
                # No other generated field is treated as validated evidence/state.
                candidate = re.sub(r"^```(?:json)?\s*|\s*```$", "", answer, flags=re.I).strip()
                if candidate.startswith("{"):
                    try:
                        wrapper = json.loads(candidate)
                        if isinstance(wrapper, dict) and isinstance(wrapper.get("answer"), str):
                            answer = wrapper["answer"].strip()
                    except (ValueError, TypeError):
                        pass
                answer = sanitize_delivery(answer)
                if not answer or len(answer) > 10000:
                    raise ModelError("invalid_answer_length")
                return ConsultationResult(
                    answer, "answered", [], round((time.monotonic() - started) * 1000),
                    lead, lead_is_ready(lead), make_summary({}, request, lead, fragments),
                    {"retrieved_sources": [f.source_id for f in fragments],
                     "context_chars": used_chars, "repairs": 0, "error_code": None,
                     "mode": "answer_only", "evidence_validation": "not_performed"})
            except ModelError as exc:
                return self._failure(request, lead, started, exc.code, fragments, http_status=exc.status)
        # Keep the current turn explicit even when it is a short answer embedded
        # in a long transcript. The global message index/evidence contract is unchanged.
        context["latest_user_message"] = {
            "index": len(request.messages) - 1, "content": request.messages[-1].content}
        context["confirmed_facts"] = confirmed_facts
        context["requested_output"] = "action_plan" if asks_for_plan(request.messages[-1].content) else "continue_consultation"
        discovery = initial_cost_discovery(request,confirmed_facts)
        meter_check = meter_reuse_question(request,fragments)
        guidance=[]
        if discovery:
            guidance.append("Это первый общий вопрос о снижении расходов, тип объекта ещё неизвестен. "
                            "Сначала задай один простой вопрос о типе объекта. Не подбирай продукты, "
                            "не перечисляй оптовый рынок/солнечные станции и не вводи числовые пороги.")
        if confirmed_facts.get('object'):
            guidance.append("Тип объекта уже указан в confirmed_facts.object. Не спрашивай тип объекта повторно.")
        if context['requested_output']=='action_plan':
            guidance.append("Последняя реплика просит конкретные действия: ответь коротким списком по sources "
                            "для указанной проблемы. Не возвращайся к общему подбору продуктов и не переноси "
                            "числовые условия ценовых категорий на выход на оптовый рынок.")
        if bill_diagnosis:
            guidance.append("Текущая задача — первые действия для разбора роста счёта известного объекта. "
                            "Клиент не заказывал проект поиска коммерческих потерь. Требования такого проекта "
                            "к годовой выгрузке и данным день/ночь не являются условиями начала этой диагностики. "
                            "Дай доступные первые действия по подходящим источникам; не добавляй обязательный "
                            "сбор данных из другого продукта.")
        if meter_check is not None:
            guidance.append("Совместимость именно существующих счётчиков клиента не подтверждена найденными "
                            "источниками. Не обещай подключение или обязательную замену. Неизвестный тип/модель "
                            "уточни для специалиста; уже названную модель повторно не спрашивай.")
        context['turn_guidance']=' '.join(guidance)
        model_messages = [{"role": "system", "content": SYSTEM_PROMPT},
                          {"role": "user", "content": json.dumps(context, ensure_ascii=False)}]
        if guidance:
            model_messages.append({"role":"system","content":"Фокус текущего хода. "+context['turn_guidance']})
        target = unconfirmed_integration_target(request.messages[-1].content, fragments)
        if target:
            # Never let generic protocol support establish a named integration.
            # Retain the normal client-fact provenance and handoff contract.
            answer = (f"В найденных материалах нет подтверждения интеграции с системой «{target}». "
                      "Это не означает, что интеграция невозможна. Для проверки нужна документация "
                      "этой системы и её интерфейсов. Можно передать вопрос специалисту.")
            summary = make_summary({}, request, lead, fragments)
            return ConsultationResult(answer, "knowledge_gap", [], round((time.monotonic()-started)*1000),
                                      lead, lead_is_ready(lead), summary,
                                      {"retrieved_sources":[f.source_id for f in fragments],
                                       "context_chars":used_chars,"repairs":0,"error_code":None,
                                       "routing":"unconfirmed_named_integration"})
        repairs = 0
        suppressed_questions = 0
        routing = None
        try:
            while True:
                try:
                    raw = self.client.complete(model_messages, deadline=deadline)
                    payload = parse_payload(raw)
                    if discovery and (re.search(r'оптов|орэм|солнеч|\d\s*(?:квт|мвт)',normalized(payload['answer'])) or
                                      not re.search(r'(?:како[йм]|что\s+за|тип).{0,35}объект|объект.{0,35}речь',normalized(payload['answer']))):
                        question='О каком объекте идёт речь: дом, кафе, офис, производство или другое?'
                        payload.update(answer='Сначала уточним объект — это поможет выбрать подходящие следующие шаги. '+question,
                                       status='clarification',citations=[])
                        payload['summary']={**payload.get('summary',{}),'manager_questions':[question],'products':[]}
                        routing='initial_object_clarification'
                    if meter_check is not None:
                        if meter_check['model_unknown']:
                            answer=('Совместимость существующих счётчиков пока не подтверждена. Это не означает, '
                                    'что их обязательно нужно менять. Тип или модель можно указать позже в сообщении '
                                    'или заявке; сейчас можно передать задачу специалисту.')
                            questions=[]; state='knowledge_gap'
                        elif meter_check['models']:
                            answer=('Указанная модель счётчика учтена. В найденных материалах нет подтверждения её '
                                    'совместимости для вашего подключения. Это не означает, что нужна замена: '
                                    'совместимость и схему подключения проверит специалист. Можно подготовить заявку.')
                            questions=[]; state='knowledge_gap'
                        else:
                            question='Какой у счётчиков тип или модель?'
                            answer=('Подключение именно ваших существующих счётчиков нужно проверить; пока нельзя '
                                    'подтвердить ни возможность работы без замены, ни необходимость замены. '+question+
                                    ' Если сейчас не знаете, укажите эти сведения позже в сообщении или заявке специалисту.')
                            questions=[question]; state='clarification'
                        payload.update(answer=answer,status=state,citations=[])
                        payload['summary']={**payload.get('summary',{}),'manager_questions':questions,'products':[]}
                        routing='unconfirmed_existing_meter'
                    sources = validate_sources(payload, fragments)
                    if confused_wholesale_threshold(payload['answer'],fragments):
                        raise ModelError('misapplied_wholesale_threshold',retryable=True)
                    if repeats_previous_answer(payload["answer"], request.messages):
                        raise ModelError("repeated_answer", retryable=True)
                    questions = re.findall(r'[^.!?\n]*\?', payload['answer'])
                    # Also catch an imperative requesting facts already provided.
                    questions += [s for s in re.split(r'(?<=[.!?])\s+|\n+', payload['answer'])
                                  if re.search(r'нужно уточнить|уточните|сообщите', s, re.I)]
                    if any(answered_topics(q, confirmed_facts) for q in questions):
                        # After the bounded repair, retain useful sourced advice
                        # while omitting the redundant request. Never show a
                        # technical error solely for an already-answered question.
                        cleaned, omitted = without_answered_questions(payload['answer'], confirmed_facts)
                        if repairs and sources and len(cleaned) >= 40:
                            payload['answer'] = cleaned
                            suppressed_questions = omitted
                            if '?' not in cleaned:
                                payload['status'] = 'answered'
                        else:
                            raise ModelError('answered_question', retryable=True)
                    if (context['requested_output'] == 'action_plan' and not routing and payload['status'] in ('answered', 'clarification')
                            and not extract_actions(payload['answer'])):
                        raise ModelError('missing_action_plan', retryable=True)
                    break
                except ModelError as exc:
                    if repairs >= 1 or not exc.retryable:
                        raise
                    repairs += 1
                    # Do not echo untrusted output back as a new instruction.
                    if exc.code in ("repeated_answer", "answered_question", "missing_action_plan"):
                        repair = ("Предыдущий результат отклонён: он дословно повторяет прошлый ответ "
                                  "ассистента, хотя клиент добавил новую реплику latest_user_message. "
                                  "Продолжи разговор с учётом этой реплики. Если клиент ответил на часть "
                                  "составного вопроса, считай эту часть известной; уточни только оставшееся "
                                  "или дай следующий полезный шаг. Не возвращай прежнее объяснение целиком. "
                                  "Верни JSON прежней схемы, сохрани проверяемые источники и факты клиента.")
                        if exc.code == 'answered_question':
                            repair = ("Предыдущий ответ снова запрашивает сведения, уже имеющиеся в confirmed_facts. "
                                      "Не спрашивай повторно известную схему снятия показаний, наличие счётчиков, режим "
                                      "или оборудование. Если составной вопрос частично закрыт, спроси только неизвестную часть. "
                                      "Ответь на latest_user_message и верни JSON прежней схемы с проверяемыми citations.")
                        elif exc.code == 'missing_action_plan':
                            repair = ("Клиент явно попросил конкретные следующие действия, но ответ их не содержит. "
                                      "Дай краткий нумерованный план по sources и confirmed_facts; каждый пункт начни "
                                      "глаголом действия. Не заменяй план очередным предложением расчёта или заявки. "
                                      "Не придумывай рекомендаций при нехватке sources: тогда status=knowledge_gap. "
                                      "Верни JSON прежней схемы с проверяемыми citations.")
                    else:
                        repair = ("Предыдущий результат отклонён валидатором: " + exc.code +
                                  ". Повтори ответ в заданном JSON. Следуй формату citations из системных правил; "
                                  "выбирай идентификаторы только из sources, не придумывай их. "
                                  "Если подтверждения нет, status=knowledge_gap. Не добавляй рассуждения.")
                    if exc.code == 'misapplied_wholesale_threshold':
                        repair=("Предыдущий ответ ошибочно перенёс 670 кВт из требования о ценовых категориях "
                                "на выход на оптовый рынок. Это разные условия. Убери этот вывод; не подставляй "
                                "иной порог без профильного подтверждения в sources. Ответь на текущую реплику "
                                "полезными действиями из подходящих источников, сохрани известный тип объекта. "
                                "Верни JSON прежней схемы с проверяемыми citations.")
                    model_messages.append({"role": "user", "content": repair})
            lead = extract_lead(payload, request)
            summary = make_summary(payload, request, lead, fragments)
            answer = payload["answer"].strip()
            # Delivery is not implemented: never let a generated claim imply it is.
            answer = sanitize_delivery(answer)
            ready_claim = re.search(r"заявк\w*\s+(?:уже\s+)?(?:готова|подготовлена|сформирована)", answer, re.I)
            if ready_claim and not lead_is_ready(lead):
                missing = ", ".join(FIELD_LABELS[f] for f in summary["missing_fields"])
                replacement = ("Можем продолжить консультацию без оформления заявки." if lead.declined
                               else "Заявка пока в черновике" + ("; осталось уточнить: " + missing if missing else "; нужно ваше подтверждение") + ".")
                answer = re.sub(r"[^.!?\n]*" + re.escape(ready_claim.group(0)) + r"[^.!?\n]*[.!?]?",
                                replacement, answer, flags=re.I)
            if len(answer) > 10000:
                raise ModelError("answer_too_long")
            return ConsultationResult(answer, payload["status"], sources,
                                      round((time.monotonic() - started) * 1000), lead,
                                      lead_is_ready(lead), summary,
                                      {"retrieved_sources": [f.source_id for f in fragments],
                                       "context_chars": used_chars, "repairs": repairs,
                                       "suppressed_known_questions": suppressed_questions,
                                       "error_code": None, **({"routing":routing} if routing else {}),
                                       **({"source_scope":"bill_diagnosis","excluded_sources":excluded_sources} if bill_diagnosis else {})})
        except ModelError as exc:
            return self._failure(request, lead, started, exc.code, fragments, repairs, http_status=exc.status)

    @staticmethod
    def _failure(request, lead, started, code, fragments=(), repairs=0, http_status=None):
        summary = make_summary({}, request, lead, fragments)
        return ConsultationResult(TECHNICAL_ANSWER, "technical_error", [],
                                  round((time.monotonic() - started) * 1000), lead,
                                  lead_is_ready(lead), summary,
                                  {"error_code": code, "http_status": http_status, "repairs": repairs,
                                   "retrieved_sources": [f.source_id for f in fragments]})
