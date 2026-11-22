const $ = selector => document.querySelector(selector);
const workspace = $('.workspace');
const queueResult = $('#queue-result');
const application = $('#application');
const metricsResult = $('#metrics-result');
const statusFilter = $('#status-filter');
const includeDemo = $('#include-demo');
const dialog = $('#action-dialog');
const dialogContent = $('#dialog-content');
const dialogError = $('#dialog-error');

const statuses = {
  new: 'Новая', in_review: 'На разборе', needs_info: 'Нужны данные',
  accepted: 'Принята в работу', rejected: 'Отклонена',
};
const facts = { confirmed: 'Подтверждено', assumed: 'Предположение', unknown: 'Неизвестно' };
const powerStates = { actual: 'Актуальное', history: 'Прежнее', unconfirmed: 'Требует подтверждения' };
const actions = {
  start_review: { label: 'Взять в работу' },
  accept: { label: 'Принять в работу' },
  needs_info: { label: 'Запросить уточнение', reason: true },
  reject: { label: 'Отклонить', reason: true },
  reopen: { label: 'Открыть повторно', reason: true },
};
const availableActions = {
  new: ['start_review'], needs_info: ['start_review'],
  in_review: ['accept', 'needs_info', 'reject'], accepted: ['reopen'], rejected: ['reopen'],
};
const contactFields = [
  ['full_name', 'Имя и фамилия'], ['phone', 'Телефон'], ['email', 'Email'], ['company', 'Компания'],
];
const dateTime = new Intl.DateTimeFormat('ru-RU', {
  dateStyle: 'short', timeStyle: 'short', timeZone: 'Europe/Moscow',
});

let selection = null;
let record = null;
let busy = false;
let pendingCommand = null;
let queueRequest = 0;
let metricsRequest = 0;
let cardRequest = 0;
let dialogRequest = 0;
let reasons;
let reasonsRequest;
let dialogTrigger;

const escape = value => String(value ?? '').replace(/[&<>"']/g, char => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
}[char]));
const items = value => Array.isArray(value) ? value : [];
const label = (labels, key, fallback) => Object.hasOwn(labels, key) ? labels[key] : fallback;
const pathFor = id => '/applications/' + encodeURIComponent(id);
const dateLabel = date => Number.isFinite(Date.parse(date)) ? dateTime.format(new Date(date)) + ' МСК' : 'Дата не указана';

async function api(path, { method = 'GET', body, download = false } = {}) {
  const response = await fetch('/api/v1' + path, {
    method, cache: 'no-store', signal: AbortSignal.timeout(20000),
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (response.ok && download) return response.blob();
  const data = await response.json().catch(() => null);
  if (response.ok && data !== null) return data;
  const error = new Error('Сервис временно недоступен.');
  error.status = response.status;
  error.code = data?.code;
  throw error;
}

function badge(labels, key, fallback) {
  const text = label(labels, key, fallback);
  const tone = Object.hasOwn(labels, key) ? key : '';
  return `<span class="badge ${tone}">${escape(text)}</span>`;
}

function actionButton(label, command, { value = '', primary = false } = {}) {
  return `<button type="button" data-command="${command}" data-value="${escape(value)}"${primary ? ' class="primary"' : ''}>${escape(label)}</button>`;
}

function feedback(message, type = '', retry = '') {
  return `<div class="feedback ${type}"><p>${escape(message)}</p>${retry ? actionButton('Повторить', retry) : ''}</div>`;
}

function urlFor(id) {
  const url = new URL(location.href);
  if (id) url.searchParams.set('id', id);
  else url.searchParams.delete('id');
  if (statusFilter.value) url.searchParams.set('status', statusFilter.value);
  else url.searchParams.delete('status');
  return url.pathname + url.search;
}

function markSelection() {
  workspace.classList.toggle('has-selection', Boolean(selection));
  for (const link of queueResult.querySelectorAll('[data-id]')) {
    if (link.dataset.id === selection) link.setAttribute('aria-current', 'true');
    else link.removeAttribute('aria-current');
  }
}

async function loadQueue() {
  const request = ++queueRequest;
  queueResult.innerHTML = feedback('Загружаем заявки…');
  try {
    const query = statusFilter.value ? '?status=' + encodeURIComponent(statusFilter.value) : '';
    const result = await api('/applications' + query);
    if (request !== queueRequest) return;
    const queue = Array.isArray(result) ? result : result.applications;
    if (!Array.isArray(queue)) throw new Error('Invalid queue response');
    if (!queue.length) {
      queueResult.innerHTML = `<p class="empty">${statusFilter.value ? 'Заявок с этим статусом пока нет.' : 'В очереди пока нет заявок.'}</p>`;
      return;
    }
    queueResult.innerHTML = '<ul class="queue-list">' + queue.map(item => {
      const customer = [item.company, item.contact_name].filter(Boolean).join(' · ');
      return `<li><a class="queue-item" href="${escape(urlFor(item.id))}" data-id="${escape(item.id)}">
        <div class="item-meta">${badge(statuses, item.status, 'Статус неизвестен')}${item.demo === true ? '<span class="badge demo">Демо</span>' : ''}<time datetime="${escape(item.updated_at)}">${dateLabel(item.updated_at)}</time></div>
        <strong>${item.number ? `№ ${escape(item.number)} · ` : ''}${escape(item.task || 'Задача не указана')}</strong>
        ${customer ? `<p class="muted">${escape(customer)}</p>` : ''}
        ${items(item.products).length ? `<p class="muted">${escape(item.products.join(' · '))}</p>` : ''}
        ${item.next_step ? `<p class="muted">${escape(item.next_step)}</p>` : ''}
        ${item.unknowns_count > 0 ? `<p class="muted">Пунктов для уточнения: ${escape(item.unknowns_count)}</p>` : ''}
      </a></li>`;
    }).join('') + '</ul>';
    markSelection();
  } catch (error) {
    if (request !== queueRequest) return;
    const unavailable = error.status === 404 || error.status === 405;
    queueResult.innerHTML = feedback(unavailable ? 'API очереди заявок ещё не подключён.' : 'Не удалось загрузить заявки.', 'error', 'queue');
  }
}

function metric(title, value, caption = '') {
  return `<div class="metric"><dt>${escape(title)}</dt><dd>${escape(value ?? '—')}</dd>${caption ? `<small>${escape(caption)}</small>` : ''}</div>`;
}

function ratioMetric(title, ratio) {
  const caption = Number.isInteger(ratio?.denominator) ? `${ratio.numerator} из ${ratio.denominator}` : '';
  return metric(title, ratio?.display, caption);
}

async function loadMetrics() {
  const request = ++metricsRequest;
  const expanded = metricsResult.querySelector('details')?.open;
  metricsResult.innerHTML = feedback('Загружаем метрики…');
  try {
    const metrics = await api('/metrics/pilot' + (includeDemo.checked ? '?include_demo=1' : ''));
    if (request !== metricsRequest) return;
    if (!Number.isInteger(metrics.sessions_with_question)) throw new Error('Invalid metrics response');
    const counts = Object.entries(statuses).map(([key, title]) =>
      `<div><dt class="muted">${title}</dt><dd>${escape(metrics.status_counts?.[key] ?? '—')}</dd></div>`);
    metricsResult.innerHTML = `<dl class="metrics-grid">
      ${metric('Диалоги с вопросом', metrics.sessions_with_question)}
      ${metric('Сохранённые заявки', metrics.sessions_with_application)}
      ${ratioMetric('Конверсия в заявку', metrics.conversion_to_application)}
      ${ratioMetric('Доля принятых в работу', metrics.acceptance_rate)}
    </dl><details class="metrics-more"${expanded ? ' open' : ''}>
      <summary>Подробнее о метриках</summary>
      <dl class="metric-details">
        ${counts.join('')}
        <div><dt class="muted">Время до заявки</dt><dd>${escape(metrics.time_to_application?.display ?? '—')}</dd></div>
        <div><dt class="muted">Время разбора</dt><dd>${escape(metrics.review_time?.display ?? '—')}</dd></div>
      </dl>
      <p class="muted">${metrics.includes_demo === true ? 'Демо-события учтены.' : 'Демо-события исключены.'}</p>
    </details>`;
  } catch {
    if (request === metricsRequest) metricsResult.innerHTML = feedback('Не удалось загрузить метрики пилота.', 'error', 'metrics');
  }
}

function evidenceLinks(evidence) {
  const sequences = [...new Set(items(evidence).map(item => item.sequence).filter(value => Number.isInteger(value) && value > 0))];
  return sequences.map(sequence => actionButton(`Сообщение №${sequence}`, 'history', { value: sequence })).join('');
}

function fact(field, title = '') {
  const status = field?.status || 'unknown';
  return `<li class="fact">${title ? `<p class="fact-label">${escape(title)}</p>` : ''}
    <p>${escape(field?.value || 'Не указано')}</p><div class="fact-meta">${badge(facts, status, 'Неизвестно')}${evidenceLinks(field?.evidence)}</div></li>`;
}

function section(title, list, { wide = false, empty = 'Данных пока нет.' } = {}) {
  return `<section class="card-section${wide ? ' wide' : ''}"><h3>${escape(title)}</h3>
    ${list.length ? `<ul class="facts">${list.join('')}</ul>` : `<p class="muted">${escape(empty)}</p>`}</section>`;
}

function cardScaffold(content) {
  application.innerHTML = `<div class="card-head"><button type="button" class="mobile-back" data-command="back">← К очереди</button>
    <h2 id="card-title" tabindex="-1">Карточка заявки</h2></div>${content}`;
}

function validRecord(data, id) {
  return data?.id === id && Number.isInteger(data.version) && data.card && typeof data.card === 'object';
}

function loadReasons() {
  if (reasons) return Promise.resolve(reasons);
  reasonsRequest ??= api('/applications/reasons').then(catalog => {
    if (!catalog || Array.isArray(catalog) || typeof catalog !== 'object' || !Object.keys(catalog).length) {
      throw new Error('Invalid reason catalog');
    }
    reasons = catalog;
    return catalog;
  }).finally(() => { reasonsRequest = undefined; });
  return reasonsRequest;
}

function renderCard(notice = '') {
  const { card, status } = record;
  const allowed = label(availableActions, status, []);
  const task = card.task?.value || 'Задача не указана';
  const reviewActions = allowed.map((name, index) => actionButton(actions[name].label, 'transition', { value: name, primary: index === 0 }));
  const contacts = contactFields.filter(([name]) => !['phone','email'].includes(name) || card.contact?.[name]?.value).map(([name, title]) => fact(card.contact?.[name], title));
  if (card.contact_time?.value) contacts.push(fact(card.contact_time, card.contact?.phone?.value ? 'Желаемое время звонка' : 'Желаемое время связи'));
  const power = items(card.power).map(value => `<li class="fact">
    <p><strong>${escape(value.amount)} ${escape(value.unit)}</strong>${value.kind?.value ? ' · ' + escape(value.kind.value) : ''}</p>
    <div class="fact-meta">${badge(powerStates, value.state, 'Состояние неизвестно')}${evidenceLinks(value.evidence)}</div>
  </li>`);
  const products = items(card.products).map(product => fact({ value: product.name, status: product.status }));
  const taskStatus = badge(facts, card.task?.status, 'Неизвестно');
  application.innerHTML = `<div class="card-head">
    <button type="button" class="mobile-back" data-command="back">← К очереди</button>
    <div class="item-meta">${badge(statuses, status, 'Статус неизвестен')}${record.demo === true ? '<span class="badge demo">Демо</span>' : ''}</div>
    <h2 id="card-title" tabindex="-1">${escape(task)}</h2>
    <div class="fact-meta">${taskStatus}${evidenceLinks(card.task?.evidence)}</div>
    <p class="muted">Заявка № ${escape(record.card?.application_number || record.form?.number || record.id)} · версия ${escape(record.version)} · ${dateLabel(record.updated_at)}</p>
    ${record.reason ? `<p class="muted">Причина: <span data-reason>${escape(reasons?.[record.reason] || 'Загружаем…')}</span></p>` : ''}
    ${record.comment ? `<p class="muted">${escape(record.comment)}</p>` : ''}
  </div>
  <div class="card-actions">
    <div class="actions">${reviewActions.join('')}${['new','in_review','needs_info'].includes(status) ? actionButton('Редактировать', 'edit') : ''}</div>
    <div class="actions">${actionButton('Скачать TXT', 'download', { value: 'txt' })}${actionButton('Скачать JSON', 'download', { value: 'json' })}</div>
  </div>
  <div id="card-feedback" aria-live="polite">${notice ? feedback(notice, 'info') : ''}</div>
  <div class="card-grid">
    ${section('Следующий шаг', [fact(card.next_step)], { wide: true })}
    ${section('Контакты', contacts)}
    ${section('Объект и параметры', ['object','parameters','constraints'].flatMap(key => items(card[key]).map(value => fact(value))))}
    ${section('Мощность и потребление', power)}
    ${section('Продукты', products)}
    ${section('Что уточнить', items(card.unknowns).map(value => fact(value)), { wide: true, empty: 'Уточнений в карточке нет.' })}
  </div>`;
  const reasonNode = application.querySelector('[data-reason]');
  if (reasonNode && !reasons?.[record.reason]) {
    const code = record.reason;
    loadReasons().then(catalog => {
      reasonNode.textContent = catalog[code] || 'Не указана в справочнике';
    }).catch(() => { reasonNode.textContent = 'Справочник причин недоступен'; });
  }
  syncControls();
}

async function loadCard(id, { focus = false, notice = '' } = {}) {
  const request = ++cardRequest;
  record = null;
  selection = id;
  markSelection();
  if (!id) {
    application.innerHTML = '<p class="empty">Выберите заявку из очереди.</p>';
    return;
  }
  cardScaffold(feedback('Загружаем карточку…'));
  try {
    const result = await api(pathFor(id));
    if (request !== cardRequest) return;
    if (!validRecord(result, id)) throw new Error('Invalid application response');
    record = result;
    renderCard(notice);
  } catch (error) {
    if (request !== cardRequest) return;
    const message = error.status === 404 ? 'Карточка заявки недоступна.' : 'Не удалось загрузить карточку.';
    cardScaffold(feedback(message, 'error', 'card'));
  }
  if (focus && request === cardRequest) $('#card-title').focus();
}

function showDialog(title, content) {
  if (!dialog.open) dialogTrigger = document.activeElement;
  $('#dialog-title').textContent = title;
  dialogContent.innerHTML = content;
  dialogError.hidden = true;
  if (!dialog.open) dialog.showModal();
}

function closeDialog() {
  ++dialogRequest;
  if (!dialog.open) return;
  dialog.close();
  if (dialogTrigger?.isConnected) dialogTrigger.focus();
  else $('#card-title')?.focus();
}

function syncControls() {
  for (const element of document.querySelectorAll('#refresh, #status-filter, #include-demo, [data-command], dialog button, dialog input, dialog select, dialog textarea')) {
    element.disabled = busy;
  }
}

async function transition(name) {
  const action = actions[name];
  if (!action.reason) return runCommand('/transitions', 'POST', { action: name });
  const request = ++dialogRequest;
  showDialog(action.label, feedback('Загружаем причины…'));
  try {
    const catalog = await loadReasons();
    if (request !== dialogRequest) return;
    const options = Object.entries(catalog).map(([code, title]) =>
      `<option value="${escape(code)}">${escape(title)}</option>`).join('');
    showDialog(action.label, `<form>
      <label>Причина<select name="reason" required><option value="">Выберите причину</option>${options}</select></label>
      <label>Комментарий <textarea name="comment" maxlength="1000"></textarea></label>
      <div class="actions"><button class="primary" type="submit">${action.label}</button></div>
    </form>`);
    dialogContent.querySelector('form').onsubmit = event => {
      event.preventDefault();
      const values = new FormData(event.currentTarget);
      runCommand('/transitions', 'POST', { action: name, reason: values.get('reason'), comment: values.get('comment').trim() });
    };
    dialogContent.querySelector('select').focus();
  } catch {
    if (request === dialogRequest) showDialog(action.label, feedback('Не удалось загрузить причины.', 'error') + actionButton('Повторить', 'transition', { value: name }));
  }
}

function editChoices(title, values, format, options, group) {
  if (!values.length) return '';
  return `<h3>${title}</h3>` + values.map((value, index) =>
    `<label>${escape(format(value))}<select name="${group}.${index}"><option value="">Оставить как есть</option>${options}</select></label>`).join('');
}

function validEmail(value) {
  if (value.length > 254 || value.split('@').length !== 2) return false;
  const [local, domain] = value.split('@');
  return local.length > 0 && local.length <= 64 && /^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+$/.test(local) &&
    !local.startsWith('.') && !local.endsWith('.') && !local.includes('..') &&
    domain.split('.').length >= 2 && domain.split('.').every(label => /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(label));
}

function openEditor() {
  const card = record.card;
  const textFields = [
    { path: 'task', title: 'Задача', value: card.task?.value || '', required: true, multiline: true },
    ...contactFields.map(([name, title]) => ({
      path: 'contact.' + name, title, value: card.contact?.[name]?.value || '', required: false,
    })),
    { path: 'next_step', title: 'Следующий шаг', value: card.next_step?.value || '', multiline: true },
  ];
  const fieldHTML = textFields.map(({ path, title, value, required, multiline }) => {
    const attrs = `name="${path}" ${required ? 'required ' : ''}`;
    const field = multiline
      ? `<textarea ${attrs}maxlength="2000">${escape(value)}</textarea>`
      : `<input ${attrs}${path === 'contact.phone' ? 'type="tel" maxlength="40"' : path === 'contact.email' ? 'type="text" inputmode="email" maxlength="254"' : 'maxlength="200"'} value="${escape(value)}">`;
    return `<label>${title}${field}</label>`;
  }).join('');
  const confirmRemove = '<option value="confirm">Подтвердить</option><option value="remove">Убрать из карточки</option>';
  const power = items(card.power);
  const powerHTML = power.map((value, index) => `<label>${escape(value.amount)} ${escape(value.unit)}
    <select name="power.${index}"><option value="">Оставить как есть</option><option value="confirm">Подтвердить как актуальное</option><option value="remove">Убрать из карточки</option></select></label>
    <label>Тип для ${escape(value.amount)} ${escape(value.unit)}
    <select name="power.${index}.kind"><option value="">Оставить как есть</option><option value="присоединённая">Присоединённая</option><option value="потребление">Потребление</option></select></label>`).join('');
  showDialog('Редактировать карточку', `<form>
    ${fieldHTML}
    <details class="editor-details"><summary>Мощность, продукты и уточнения</summary>
      <div class="editor-fields">
        ${power.length ? '<h3>Мощность и потребление</h3>' + powerHTML : ''}
        <label>Добавить значение<input name="power" placeholder="Например, 600 кВт" maxlength="2000"></label>
        ${editChoices('Продукты', items(card.products), product => product.name, confirmRemove, 'products')}
        ${editChoices('Уточнения', items(card.unknowns), item => item.value, '<option value="remove">Убрать из карточки</option>', 'unknowns')}
        <label>Добавить уточнение<input name="unknowns" maxlength="2000"></label>
      </div>
    </details>
    <div class="actions"><button class="primary" type="submit">Сохранить изменения</button></div>
  </form>`);
  const form = dialogContent.querySelector('form');
  for (const name of ['contact.phone', 'contact.email']) form.elements.namedItem(name).addEventListener('input', () => {
    for (const field of ['contact.phone','contact.email']) form.elements.namedItem(field).setCustomValidity('');
  });
  form.onsubmit = event => {
    event.preventDefault();
    const values = new FormData(form);
    const phone = form.elements.namedItem('contact.phone');
    const email = form.elements.namedItem('contact.email');
    const phoneValue = phone.value.trim(), emailValue = email.value.trim();
    const digits = phoneValue.replace(/\D/g, '').length;
    if (!phoneValue && !emailValue) {
      phone.setCustomValidity('Оставьте хотя бы один контакт: телефон или email.');
      phone.reportValidity();
      return;
    }
    if (emailValue && !validEmail(emailValue)) {
      email.setCustomValidity('Укажите корректный email, например name@example.ru.');
      email.reportValidity();
      return;
    }
    if (phoneValue && (!/^\+?[0-9 ()-]+$/.test(phoneValue) || digits < 10 || digits > 15 || phoneValue.length > 40)) {
      phone.setCustomValidity('Укажите номер телефона из 10–15 цифр.');
      phone.reportValidity();
      return;
    }
    const edits = [];
    for (const field of textFields) {
      const value = values.get(field.path).trim();
      if (value === field.value.trim()) continue;
      if (value) edits.push({ path: field.path, action: 'set', value });
      else if (['contact.company','contact.full_name','contact.phone','contact.email'].includes(field.path)) edits.push({ path: field.path, action: 'remove' });
      else {
        setDialogError(`Заполните поле «${field.title}».`);
        form.elements.namedItem(field.path).focus();
        return;
      }
    }
    for (const [path, input] of values) {
      if (!input.trim()) continue;
      if (path === 'power' || path === 'unknowns') edits.push({ path, action: 'add', value: input.trim() });
      if (/^(power|products|unknowns)\.\d+$/.test(path)) edits.push({ path, action: input });
      if (/^power\.\d+\.kind$/.test(path) && values.get(path.slice(0, -5)) !== 'remove') {
        edits.push({ path, action: 'set', value: input });
      }
    }
    if (!edits.length || edits.length > 50) {
      setDialogError(edits.length ? 'За один раз можно сохранить до 50 изменений.' : 'Изменений для сохранения нет.');
      return;
    }
    runCommand('/card', 'PATCH', { actor: 'manager', edits });
  };
}

function setDialogError(message) {
  dialogError.hidden = false;
  dialogError.textContent = message;
}

function showCommandError(message) {
  if (dialog.open) setDialogError(message);
  else $('#card-feedback').innerHTML = feedback(message, 'error');
}

async function runCommand(suffix, method, fields) {
  if (busy || !record) return;
  const id = record.id;
  const path = pathFor(id) + suffix;
  const payload = { expected_version: record.version, ...fields };
  const signature = JSON.stringify([path, method, payload]);
  if (pendingCommand?.signature !== signature) {
    pendingCommand = { signature, requestID: crypto.randomUUID() };
  }
  const command = pendingCommand;
  busy = true;
  syncControls();
  try {
    const updated = await api(path, { method, body: { ...payload, client_request_id: command.requestID } });
    if (!validRecord(updated, id)) throw new Error('Invalid application response');
    pendingCommand = null;
    if (selection === id) {
      ++cardRequest;
      record = updated;
      renderCard('Изменения сохранены.');
      closeDialog();
      $('#card-title').focus();
    }
    await Promise.all([loadQueue(), loadMetrics()]);
  } catch (error) {
    if (error.status >= 400 && error.status < 500 && ![408, 429].includes(error.status)) pendingCommand = null;
    if (selection !== id) return;
    if (error.code === 'version_conflict' || error.code === 'application_closed' || error.code === 'invalid_transition') {
      closeDialog();
      await Promise.all([
        loadCard(id, { focus: true, notice: 'Заявка изменилась. Проверьте новую версию перед следующим действием.' }),
        loadQueue(), loadMetrics(),
      ]);
    } else {
      const messages = {
        invalid_edit: 'Проверьте значения в полях карточки.',
        reason_required: 'Выберите причину действия.',
        request_conflict: 'Этот запрос уже использован. Обновите карточку перед повтором.',
      };
      showCommandError(label(messages, error.code, 'Не удалось подтвердить действие. Повторите его с теми же данными.'));
    }
  } finally {
    busy = false;
    syncControls();
  }
}

async function download(format, button) {
  const id = record.id;
  button.disabled = true;
  try {
    const content = await api(pathFor(id) + '/export?format=' + format, { download: true });
    const url = URL.createObjectURL(content);
    const link = document.createElement('a');
    link.href = url;
    link.download = `energytool-application-${id}.${format}`;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch {
    if (selection === id) showCommandError('Не удалось скачать файл. Попробуйте ещё раз.');
  } finally {
    button.disabled = busy;
  }
}

async function showHistory(sequence) {
  const request = ++dialogRequest;
  const session = record.session_id || record.card.session_id;
  showDialog('История диалога', feedback('Загружаем переписку…'));
  try {
    const result = await api('/sessions/' + encodeURIComponent(session) + '/messages');
    if (request !== dialogRequest) return;
    const messages = items(result.messages);
    const transcript = messages.map(message => `<li${message.sequence === sequence ? ' class="highlight" tabindex="-1"' : ''}>
      <p class="muted">№${escape(message.sequence)} · ${message.role === 'user' ? 'Клиент' : 'Консультант'}</p>
      <p>${escape(message.content)}</p></li>`).join('');
    const found = messages.some(message => message.sequence === sequence);
    showDialog('История диалога', (found ? '' : feedback('Указанная реплика отсутствует в истории.')) + `<ol class="transcript">${transcript}</ol>`);
    dialogContent.querySelector('.highlight')?.focus();
  } catch {
    if (request === dialogRequest) showDialog('История диалога', feedback('Не удалось загрузить переписку.', 'error', ''));
  }
}

function navigate(id, focus = false) {
  history.pushState(null, '', urlFor(id));
  loadCard(id, { focus });
}

function restoreLocation() {
  const params = new URLSearchParams(location.search);
  const status = params.get('status');
  statusFilter.value = Object.hasOwn(statuses, status) ? status : '';
  closeDialog();
  loadQueue();
  loadCard(params.get('id'));
}

queueResult.addEventListener('click', event => {
  const link = event.target.closest('[data-id]');
  if (!link || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  if (!busy) navigate(link.dataset.id, true);
});

document.addEventListener('click', event => {
  const button = event.target.closest('[data-command], [data-close-dialog]');
  if (!button || button.disabled || busy) return;
  if (button.hasAttribute('data-close-dialog')) return closeDialog();
  const { command, value } = button.dataset;
  if (command === 'queue') return loadQueue();
  if (command === 'metrics') return loadMetrics();
  if (command === 'card') return loadCard(selection, { focus: true });
  if (command === 'back') {
    const selected = queueResult.querySelector('[aria-current]');
    navigate(null);
    (selected || statusFilter).focus();
    return;
  }
  if (!record) return;
  if (command === 'edit') return openEditor();
  if (command === 'transition' && label(availableActions, record.status, []).includes(value)) return transition(value);
  if (command === 'download') return download(value, button);
  if (command === 'history') return showHistory(Number(value));
});

dialog.addEventListener('cancel', event => {
  event.preventDefault();
  if (!busy) closeDialog();
});
$('#refresh').addEventListener('click', () => {
  loadQueue();
  loadMetrics();
  loadCard(selection);
});
statusFilter.addEventListener('change', () => {
  history.replaceState(null, '', urlFor(selection));
  loadQueue();
});
includeDemo.addEventListener('change', loadMetrics);
window.addEventListener('popstate', restoreLocation);
restoreLocation();
loadMetrics();
