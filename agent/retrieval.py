"""Replaceable lexical baseline; Artem can implement the Retriever protocol.

Reads every Markdown section at startup. No corpus, embeddings or index is shipped.
No calls to external services and no archive extraction to disk.
"""
from collections import Counter
from dataclasses import replace
import json
import math
from pathlib import Path
from pathlib import PurePosixPath
import re
from zipfile import ZipFile

from contracts import Fragment

STOP = set("это как что для или при уже есть нет нам вас ваш нас все так его она они мы вы и в на с по к из от до за а но у о ли бы не то же со во еще чем чтобы который какая какие можно нужно".split())
STOP.update("надо сначала потом вообще отдельно просто почти теперь только также да нет стоит сможете сможет можно можем хотите хотим хочу должен будут будет такие такой этого этом сего своего нашем вашей наша наш вашим увы работает работать поставит поставить менять изменит получится разберет увидеть видеть посмотреть сначала начать понимаем понял примерно около больше всего очень несколько которых благодаря сказать сказали вопрос подскажите расскажите пожалуйста пользователь ассистент контекст разговора".split())

# Vocabulary normalization only, never product facts or canned answers.
ALIASES = (
    (r"сим[ -]?карт|симк|\bsim\b", "simcard"),
    (r"крыш|кровл", "roof"),
    (r"фасад|вертикальн", "facade"),
    (r"расшир|наращ|нараст|поэтап", "expansion"),
    (r"магазин|ретейл|ритейл", "retail"),
    (r"двухтариф|многотариф|тарифн[а-я]*\s+зон|раздельно\s+по\s+тариф", "multitariff"),
    (r"фото|изображен|снимк", "photo"),
    (r"счетчик|прибор[а-я]*\s+учет", "meter"),
    (r"компенс[а-я]*\s+реактив|реактивн", "reactive"),
    (r"провер[а-я]*\s+(?:наш|существующ|действующ)?[а-я ]*учет|аудит", "audit"),
    (r"год[а-я]*\s+(?:выпуска|производства|изготовления)", "manufacture_year"),
    (r"арендатор|арендодател", "tenant"),
    (r"цех|подразделен", "workshop"),
    (r"обход|ручн[а-я]*\s+(?:ввод|сбор)|\bтехник\b|контролер", "manual_collection"),
    (r"что\s+(?:нужно|понадоб)|какие\s+(?:данн|свед)|что\s+нужно\s+от\s+клиента", "required_inputs"),
    (r"обратно|вернут|возврат", "return"),
    (r"\bвод[а-я]{0,4}\b|водоснабж", "water"),
    (r"\bгаз[а-я]{0,4}\b", "gas"),
)

# Query vocabulary, not product facts. These only bias ranking; every document
# remains searchable, including questions that compare several products.
TOPICS = {
    "solar": r"солнеч|панел|сэс\b|фотоэлектр|кровл|фасад|сетев[а-я]*\s+станц",
    "orem": r"оптов|орэм\b",
    "photo": r"распознав|фотограф|\bфото\b|снимк",
    "losses": r"потер[ьи]|хищен|воровств",
    "metering": r"аскуэ|платформ|кабинет|арендатор|цех|магазин|автоматическ[а-я]*\s+учет",
}


def topics(text):
    normalized = text.lower().replace("ё", "е")
    return {name for name, pattern in TOPICS.items() if re.search(pattern, normalized)}


def tokens(text):
    text = text.lower().replace("ё", "е")
    words = re.findall(r"[а-яa-z0-9]+", text)
    result = ["@" + canonical for pattern, canonical in ALIASES if re.search(pattern, text)]
    for word in words:
        if word in STOP or len(word) < 2:
            continue
        result.append(word)
        if len(word) >= 5 and re.search("[а-я]", word):
            result.append("~" + word[:5])
    return result


def load_fragments(archive_path):
    fragments = []
    with ZipFile(archive_path) as archive:
        members = [m for m in archive.infolist()
                   if not m.is_dir() and PurePosixPath(m.filename).suffix.lower() in (".md", ".txt")
                   and not any(p.startswith((".", "__MACOSX")) for p in PurePosixPath(m.filename).parts)]
        if len(members) > 2000 or sum(m.file_size for m in members) > 50_000_000:
            raise ValueError("Knowledge base exceeds size limit")
        names = set()
        for member in sorted(members, key=lambda m: m.filename):
            name = member.filename
            if (PurePosixPath(name).is_absolute() or ".." in PurePosixPath(name).parts
                    or "\\" in name or name in names or member.file_size > 5_000_000):
                raise ValueError("Invalid knowledge base member")
            names.add(name)
            text = archive.read(member).decode("utf-8-sig")
            text = text.replace("\\## ", "\n## ")
            heading = []
            section = []
            counter = 0

            def emit():
                nonlocal counter
                body = "\n".join(section).strip()
                if not body:
                    return
                title = " > ".join(heading)
                # Keep ordinary sections/tables intact. Split only very long sections.
                while body:
                    cut = min(len(body), 4800)
                    if cut < len(body):
                        boundary = body.rfind("\n\n", 1800, cut)
                        if boundary > 0:
                            cut = boundary
                    piece, body = body[:cut].strip(), body[cut:].lstrip()
                    counter += 1
                    fragments.append(Fragment(name, f"s{counter:03d}", title + "\n" + piece))

            for line in text.splitlines():
                match = re.match(r"^(#{1,6})\s+(.+)", line)
                if match:
                    emit()
                    section = []
                    level = len(match[1])
                    heading = heading[:level - 1] + [match[2]]
                elif not re.fullmatch(r"\s*-{8,}\s*", line):
                    section.append(line)
            emit()
    if not fragments:
        raise ValueError("Knowledge base has no readable Markdown/text content")
    return fragments


def load_expert_snapshot(snapshot_path):
    """Load a complete, validated set of approved web-only knowledge documents.

    The Go operator API writes this versioned JSON atomically. This loader never
    accepts request/session/contact fields and never replaces the base archive's
    fragments. Every document is converted through the same Markdown section
    rules as the supplied knowledge base, so citations remain factual.
    """
    path = Path(snapshot_path)
    raw = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(raw, dict) or not isinstance(raw.get("kb_version"), str) or not raw["kb_version"].strip():
        raise ValueError("Expert snapshot must contain a non-empty kb_version")
    documents = raw.get("documents")
    if not isinstance(documents, list) or len(documents) > 500:
        raise ValueError("Expert snapshot documents are invalid")
    fragments = []
    seen = set()
    for document in documents:
        if not isinstance(document, dict):
            raise ValueError("Expert snapshot document is invalid")
        allowed = {"entry_id", "revision", "path", "source_id", "markdown", "test"}
        if set(document) - allowed:
            raise ValueError("Expert snapshot contains unsupported fields")
        entry_id = document.get("entry_id")
        revision = document.get("revision")
        name = document.get("path")
        source_id = document.get("source_id")
        markdown = document.get("markdown")
        if (not isinstance(entry_id, str) or not entry_id or not isinstance(revision, int) or revision < 1
                or not isinstance(name, str) or not name.startswith("90-expert/")
                or not isinstance(source_id, str) or not isinstance(markdown, str) or not markdown.strip()
                or len(markdown) > 5000):
            raise ValueError("Expert snapshot document fields are invalid")
        if ".." in Path(name).parts or "\\" in name or not source_id.startswith(name + "#"):
            raise ValueError("Expert snapshot source path is invalid")
        key = (entry_id, revision, source_id)
        if key in seen:
            raise ValueError("Expert snapshot contains duplicate source")
        seen.add(key)
        # Keep the source ID from the Go renderer stable; the section loader
        # itself must produce exactly one section for each approved document.
        loaded = _load_markdown_fragments(name, markdown)
        if len(loaded) != 1 or loaded[0].source_id != source_id:
            raise ValueError("Expert snapshot document must contain one stable section")
        fragments.extend(loaded)
    return raw["kb_version"], fragments


def _load_markdown_fragments(name, text):
    fragments = []
    heading, section, counter = [], [], 0

    def emit():
        nonlocal counter
        body = "\n".join(section).strip()
        if not body:
            return
        title = " > ".join(heading)
        while body:
            cut = min(len(body), 4800)
            if cut < len(body):
                boundary = body.rfind("\n\n", 1800, cut)
                if boundary > 0:
                    cut = boundary
            piece, body = body[:cut].strip(), body[cut:].lstrip()
            counter += 1
            fragments.append(Fragment(name, f"s{counter:03d}", title + "\n" + piece))

    for line in text.replace("\\## ", "\n## ").splitlines():
        match = re.match(r"^(#{1,6})\s+(.+)", line)
        if match:
            emit()
            section = []
            level = len(match[1])
            heading = heading[:level - 1] + [match[2]]
        elif not re.fullmatch(r"\s*-{8,}\s*", line):
            section.append(line)
    emit()
    return fragments


class LexicalRetriever:
    """Field-aware BM25 with soft topic routing and relevant companion evidence."""
    def __init__(self, fragments):
        self.fragments = tuple(fragments)
        # A product name repeated in every ancestor heading must not swamp the
        # actual evidence. Index section bodies and the specific leaf separately.
        self.counts = [Counter(tokens(f.text.split("\n", 1)[-1])) for f in self.fragments]
        self.headings = [Counter(tokens(" > ".join(f.text.split("\n", 1)[0].split(" > ")[1:])
                                         or f.text.split("\n", 1)[0])) for f in self.fragments]
        self.lengths = [sum(c.values()) for c in self.counts]
        self.average = sum(self.lengths) / max(1, len(self.lengths))
        self.df = Counter(token for counts in self.counts for token in counts)
        self.fragment_topics = []
        for fragment in self.fragments:
            heading = fragment.text.split("\n", 1)[0]
            # References contain one subject per section; product documents
            # carry their subject in the root heading, not incidental cross-links.
            subject = heading if fragment.document_id.startswith("00-reference/") else heading.split(" > ")[0]
            self.fragment_topics.append(topics(subject))

    @classmethod
    def from_zip(cls, path):
        return cls(load_fragments(path))

    @classmethod
    def from_zip_and_snapshot(cls, path, snapshot_path=None):
        fragments = load_fragments(path)
        kb_version = "base"
        if snapshot_path:
            kb_version, extra = load_expert_snapshot(snapshot_path)
            fragments.extend(extra)
        retriever = cls(fragments)
        retriever.kb_version = kb_version
        return retriever

    def search(self, query, top_k=12):
        if top_k <= 0:
            return []
        current, _, history = query.partition("\nКонтекст разговора:\n")
        # dialogue.retrieval_query repeats the current turn for the old scorer.
        # Weight distinct current terms once and retain earlier customer facts.
        query_counts = Counter({word: 1.0 for word in tokens(current)})
        current_topics = topics(current)
        explicit_switch = bool(current_topics and re.search(
            r"вернемся|сменим\s+тему|(?:теперь|сейчас)\s+(?:другая|другой|другую|новая|новый)\b",
            current.lower().replace("ё", "е")))
        for word in set(tokens(history)):
            query_counts[word] += .15 if explicit_switch else .65
        active_topics = current_topics if explicit_switch else current_topics | topics(history)
        numeric_question = bool(re.search(
            r"скольк|мощн|квт|мвт|процент|цен[аыуе]|стоим|окуп|тариф|порог|срок|гарант|\bлет\b|\bгод|руб|\d",
            query.lower().replace("ё", "е")))
        scores = []
        total = len(self.fragments)
        for i, counts in enumerate(self.counts):
            score = 0.0
            for token, weight in query_counts.items():
                freq = counts.get(token, 0)
                heading_freq = self.headings[i].get(token, 0)
                if not freq and not heading_freq:
                    continue
                idf = math.log(1 + (total - self.df[token] + 0.5) / (self.df[token] + 0.5))
                denominator = freq + 1.5 * (0.55 + 0.45 * self.lengths[i] / max(1, self.average))
                boost = (0.55 if token.startswith("~") else
                         1.0 if token == "@meter" else
                         2.0 if token == "@photo" else
                         5.0 if token.startswith("@") else 1.0)
                body_score = freq * 2.5 / denominator
                score += idf * (body_score + 1.8 * min(heading_freq, 1)) * weight * boost
            if active_topics:
                overlap = self.fragment_topics[i] & active_topics
                if overlap:
                    score *= 1.8
                elif self.fragment_topics[i]:
                    score *= .35
            if "01-numbers-and-indicators" in self.fragments[i].document_id and not numeric_question:
                score *= .3
            if score:
                scores.append((score, i))
        scores.sort(key=lambda item: (-item[0], self.fragments[item[1]].source_id))
        selected = [replace(self.fragments[i], score=round(score, 4)) for score, i in scores[:top_k]]
        # Companion restrictions prevent isolated attractive numbers losing their caveats.
        primary_docs = list(dict.fromkeys(f.document_id for f in selected[:4]))[:2]
        guards = []
        for document in primary_docs:
            guards.extend(f for f in self.fragments if f.document_id == document
                          and any(heading in f.text.split("\n", 1)[0] for heading in
                                  ("Чего утверждать нельзя", "Что нужно от клиента", "Важное ограничение", "Не подходит, если")))
        # Keep the matching product's scope and required inputs, rather than
        # adding every product's restrictions and contact workflow to every turn.
        support = []
        relevant_topics = active_topics or set().union(*(self.fragment_topics[i] for _, i in scores[:3]))
        for i, fragment in enumerate(self.fragments):
            if not fragment.document_id.startswith("00-reference/"):
                continue
            if self.fragment_topics[i] & relevant_topics and any(part in fragment.document_id for part in
                    ("02-product-scope", "03-calculation")):
                support.append(fragment)
            elif ("03-calculation" in fragment.document_id
                  and "Бесплатная предварительная оценка" in fragment.text.split("\n", 1)[0]
                  and re.search(r"оценк|расчет|невыгод|стоим|эконом", query.lower().replace("ё", "е"))):
                support.append(fragment)
        result, seen = [], set()
        for fragment in selected + guards + support:
            if fragment.source_id not in seen:
                result.append(fragment)
                seen.add(fragment.source_id)
        return result


class FixedRetriever:
    """For manual real-KB fragments or deterministic integration tests."""
    def __init__(self, fragments):
        self.fragments = tuple(fragments)

    def search(self, query, top_k=12):
        return self.fragments[:top_k]
