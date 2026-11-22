"""High-recall lexical retrieval without external services.

Two independent rankings are fused (reciprocal rank fusion):
  1. the field-aware BM25 scorer of the protected A2 baseline (retrieval_a2);
  2. a character n-gram TF-IDF scorer that survives colloquial word forms
     ("опт" -> "оптовый", "симки" -> "сим-карт").
The selected sections are completed with the rule sections of the products
they belong to, so numbers never travel without their caveats.
No corpus text, product fact or canned answer is stored in this module.
"""
from collections import Counter, defaultdict
import math
import re

from retrieval import LexicalRetriever, load_fragments

RULE_HEADINGS = ("Чего утверждать нельзя", "Факты, которые можно утверждать", "Что мы не делаем",
                 "Не подходит, если", "Важное ограничение", "Что нужно от клиента")
# Reference sections are keyed by the product they describe (heading vocabulary only).
PRODUCT_KEYS = {
    "20-products/21-orem.md": r"оптов",
    "20-products/22-askue.md": r"платформ[а-я]* учет",
    "20-products/23-solar.md": r"солнечн",
    "20-products/24-losses.md": r"коммерческ[а-я]* потер",
    "20-products/25-meter-reading.md": r"распознаван",
}
STATIC_DOCS = re.compile(r"(?:^|/)\d*-?(?:portfolio|general)\.(?:md|txt)$")
CONTACT = re.compile(r"телефон|контакт|позвон|перезвон|свя(?:ж|з)|заявк|менеджер|специалист|номер|\+?\d[\d ()\-]{8,}\d",
                     re.I)
WORD = re.compile(r"[а-яa-z0-9]+")


def _norm(text):
    return text.lower().replace("ё", "е")


def _grams(text):
    """Character 3..5-grams inside padded words; digits are kept as whole tokens."""
    out = []
    for word in WORD.findall(_norm(text)):
        if word.isdigit():
            out.append("#" + word)
            continue
        if len(word) < 3:
            continue
        padded = "^" + word + "$"
        for n in (3, 4, 5):
            for i in range(len(padded) - n + 1):
                out.append(padded[i:i + n])
    return out


class WideRetriever:
    def __init__(self, fragments):
        self.fragments = tuple(fragments)
        self.base = LexicalRetriever(self.fragments)
        self.index = {f.source_id: i for i, f in enumerate(self.fragments)}
        self.headings = [f.text.split("\n", 1)[0] for f in self.fragments]
        docs = []
        for f, heading in zip(self.fragments, self.headings):
            leaf = " ".join(heading.split(" > ")[1:]) or heading
            body = f.text.split("\n", 1)[-1]
            docs.append(Counter(_grams(leaf + " " + leaf + " " + body)))
        total = len(docs)
        df = Counter(g for d in docs for g in d)
        self.idf = {g: math.log(1 + (total - n + 0.5) / (n + 0.5)) for g, n in df.items()}
        self.vectors, self.norms = [], []
        for d in docs:
            vec = {g: (1 + math.log(c)) * self.idf[g] for g, c in d.items()}
            self.vectors.append(vec)
            self.norms.append(math.sqrt(sum(v * v for v in vec.values())) or 1.0)
        postings = defaultdict(list)
        for i, vec in enumerate(self.vectors):
            for g, v in vec.items():
                postings[g].append((i, v))
        self.postings = dict(postings)   # read-only after construction (shared by worker threads)

    @classmethod
    def from_zip(cls, path):
        return cls(load_fragments(path))

    # ---- rankings -------------------------------------------------------
    def _gram_ranking(self, current, history):
        weights = Counter()
        for g, c in Counter(_grams(current)).items():
            if g in self.idf:
                weights[g] += (1 + math.log(c)) * self.idf[g]
        for g in set(_grams(history)):
            if g in self.idf:
                weights[g] += 0.3 * self.idf[g]
        scores = defaultdict(float)
        for g, w in weights.items():
            for i, v in self.postings[g]:
                scores[i] += w * v
        ranked = sorted(((s / self.norms[i], i) for i, s in scores.items()),
                        key=lambda item: (-item[0], self.fragments[item[1]].source_id))
        return [i for _, i in ranked]

    def _word_ranking(self, query):
        scored = [f for f in self.base.search(query, len(self.fragments)) if f.score > 0]
        return [self.index[f.source_id] for f in scored]

    def rank(self, query):
        """Fused ranking of fragment indexes for an A2-style retrieval query."""
        current, _, history = query.partition("\nКонтекст разговора:\n")
        current = current.split("\n", 1)[0] if current.count("\n") == 1 and \
            current.split("\n")[0] == current.split("\n")[1] else current
        fused = defaultdict(float)
        for weight, ranking in ((1.0, self._word_ranking(query)), (1.0, self._gram_ranking(current, history))):
            for position, i in enumerate(ranking[:60]):
                fused[i] += weight / (20 + position)
        return [i for i, _ in sorted(fused.items(), key=lambda kv: (-kv[1], self.fragments[kv[0]].source_id))]

    # ---- context assembly -----------------------------------------------
    def _companions(self, ranked, query):
        """Rule and reference sections for the products that lead the ranking."""
        lead_docs = []
        for i in ranked[:6]:
            doc = self.fragments[i].document_id
            if doc in PRODUCT_KEYS and doc not in lead_docs:
                lead_docs.append(doc)
        lead_docs = lead_docs[:2]
        extra = []
        for doc in lead_docs:
            key = re.compile(PRODUCT_KEYS[doc])
            for i, f in enumerate(self.fragments):
                heading = self.headings[i]
                if f.document_id == doc and any(h in heading for h in RULE_HEADINGS):
                    extra.append(i)
                elif f.document_id.startswith("00-reference/") and "00-product-map" not in f.document_id \
                        and key.search(_norm(" > ".join(heading.split(" > ")[1:]))):
                    extra.append(i)
        for i, f in enumerate(self.fragments):
            heading = self.headings[i]
            if "02-product-scope" in f.document_id and ("Общие границы" in heading or "Когда требуется специалист" in heading):
                extra.append(i)
            elif "03-calculation" in f.document_id and CONTACT.search(query) and \
                    ("Обратный звонок" in heading or "Заявка в рамках" in heading):
                extra.append(i)
        return extra

    def search(self, query, top_k=10, budget_chars=14000):
        ranked = self.rank(query)
        chosen, used = [], 0
        for i in ranked[:top_k] + self._companions(ranked, query):
            f = self.fragments[i]
            if i in chosen or used + len(f.text) > budget_chars:
                continue
            chosen.append(i)
            used += len(f.text)
        return [self.fragments[i] for i in chosen]

    @staticmethod
    def is_static(document_id):
        """Documents every answer may need: the reference rules and numbers, the product overview
        (which product fits which task and company) and the general questions."""
        return document_id.startswith("00-reference/") or bool(STATIC_DOCS.search(document_id))

    def reference(self):
        return [f for f in self.fragments if self.is_static(f.document_id)]

    def search_documents(self, query, documents=2, top_k=8, budget_chars=60000):
        """Whole documents that lead the ranking plus the best sections of the others.

        Reference documents are excluded here: the caller places them in the static prompt.
        """
        ranked = self.rank(query)
        score = defaultdict(float)
        for position, i in enumerate(ranked[:30]):
            doc = self.fragments[i].document_id
            if not self.is_static(doc):
                score[doc] += 1.0 / (10 + position)
        lead = [doc for doc, _ in sorted(score.items(), key=lambda kv: (-kv[1], kv[0]))][:documents]
        chosen, used = [], 0

        def add(i):
            nonlocal used
            f = self.fragments[i]
            if self.is_static(f.document_id) or i in chosen or used + len(f.text) > budget_chars:
                return
            chosen.append(i)
            used += len(f.text)

        for i in ranked[:top_k]:
            add(i)
        for doc in lead:
            for i, f in enumerate(self.fragments):
                if f.document_id == doc:
                    add(i)
        for i in self._companions(ranked, query):
            add(i)
        return self.ordered([self.fragments[i] for i in chosen])

    def ordered(self, fragments):
        """Reading order: documents by first appearance, sections in source order."""
        first = {}
        for position, f in enumerate(fragments):
            first.setdefault(f.document_id, position)
        return sorted(fragments, key=lambda f: (first[f.document_id], self.index[f.source_id]))

class WebRetriever:
    """N2 ranking/document context adapted to the verified web JSON contract.

    The frozen N2 submission is unchanged. Its platform score does not measure
    this adapter or the local model. Snapshots are replaced per request atomically.
    """
    def __init__(self, knowledge_path, *, mode="n2", context_chars=32000, snapshot_path=None):
        import threading
        self.base_fragments = tuple(load_fragments(knowledge_path))
        if mode not in ("a2", "n2"):
            raise ValueError("WEB_RETRIEVAL_MODE must be a2 or n2")
        self.mode, self.budget = mode, context_chars
        self.snapshot_path = snapshot_path
        self.kb_version = "base"
        self.snapshot_error = False
        self._stamp = None
        self._lock = threading.Lock()
        self.engine = self._build(self.base_fragments)

    def _build(self, fragments):
        return WideRetriever(fragments) if self.mode == "n2" else LexicalRetriever(fragments)

    def _current(self):
        from pathlib import Path
        from retrieval import load_expert_snapshot
        if not self.snapshot_path:
            return self.engine
        with self._lock:
            path = Path(self.snapshot_path)
            try:
                stat = path.stat()
                stamp = (stat.st_mtime_ns, stat.st_size)
            except FileNotFoundError:
                stamp = (0, 0)
            if stamp != self._stamp:
                extra, version, error = [], "base", False
                if stamp != (0, 0):
                    try:
                        version, extra = load_expert_snapshot(path)
                    except (OSError, ValueError, TypeError):
                        error = True  # Never keep revoked/invalid old expert knowledge.
                self.engine = self._build(self.base_fragments + tuple(extra))
                self.kb_version, self.snapshot_error, self._stamp = version, error, stamp
            return self.engine

    def search(self, query, top_k=12):
        engine = self._current()
        if not query.strip() or top_k <= 0:
            return []
        if self.mode == "a2":
            return engine.search(query, top_k)
        ranked = engine.rank(query)
        # Keep N2's lexical fusion and document expansion. For interactive turns,
        # include relevant companion rules instead of every unrelated reference.
        reference_ids = list(dict.fromkeys(engine._companions(ranked, query) + ranked[:top_k]))
        chosen, used = [], 0
        for i in reference_ids:
            f = engine.fragments[i]
            if engine.is_static(f.document_id) and used + len(f.text) <= self.budget // 2:
                chosen.append(f)
                used += len(f.text)
        selected = engine.search_documents(query, documents=2, top_k=min(top_k, 6), budget_chars=self.budget-used)
        return chosen + selected
