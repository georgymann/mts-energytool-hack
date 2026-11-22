(() => {
  'use strict';
  // Open the consultation from the site's existing CTA, including dynamic pages.
  if (!document.body.classList.contains('et-app')) {
    document.addEventListener('click', event => {
      const button = event.target.closest('#root button');
      if (!button || button.textContent.trim() !== 'Заказать консультацию') return;
      event.preventDefault();
      event.stopImmediatePropagation();
      location.href = '/consultation.html?return=' +
        encodeURIComponent(location.pathname + location.search + location.hash);
    }, true);
    return;
  }
  const host = document.getElementById('energytool-chat');
  const $ = s => host.querySelector(s);
  const messages = $('.et-messages'), flow = $('.et-flow'), scroll = $('.et-scroll');
  const input = $('#et-question'), modal = $('.et-modal'), modalBody = $('.et-modal-body');
  const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const button = (label, action, primary = false) => `<button type="button" data-action="${action}"${primary ? ' class="et-primary"' : ''}>${label}</button>`;
  let current = null, busy = false, needsSync = false, debug = false, draft = null, stage = 'chat', notice = '', modalFocus = null, selectedSource = null, archived = null, noPhone = false, collapsedAt = 0, pending = false;
  let unconfirmedMessage = null;
  let lastShownAnswer = '';
  let selectedSourceContext = 'current';
  const responseLabels = new Map([
    ['answered', 'Получен ответ'],
    ['clarification', 'Нужно уточнение'],
    ['knowledge_gap', 'Нет данных в базе'],
    ['out_of_scope', 'Вопрос вне темы'],
    ['technical_error', 'Технический сбой'],
  ]);
  let savedID;
  try { savedID = localStorage.getItem('energytool-session'); } catch (_) {}
  let returnPath = '/';
  const candidate = new URLSearchParams(location.search).get('return');
  if (candidate) { try { const url = new URL(candidate, location.origin); if (url.origin === location.origin && url.pathname !== '/consultation.html') returnPath = url.pathname + url.search + url.hash; } catch (_) {} }
  $('.et-brand').href = returnPath;
  let knownIDs = new Set();
  try { knownIDs = new Set(JSON.parse(localStorage.getItem('energytool-session-ids') || '[]')); } catch (_) {}
  if(savedID) knownIDs.add(savedID);
  const remember = id => { if(id) { knownIDs.add(id); try { localStorage.setItem('energytool-session-ids',JSON.stringify([...knownIDs])); } catch (_) {} } savedID = id; try { id ? localStorage.setItem('energytool-session',id) : localStorage.removeItem('energytool-session'); } catch (_) {} };
  const card = (title, body, actions, cls = '') => `<section class="et-card ${cls}"><h2>${title}</h2>${body}<div class="et-stack">${actions}</div></section>`;
  const assistant = text => messageHTML({role:'assistant',content:text});
  const previous = () => button('Предыдущая переписка','transcript');
  const isClosed = () => current?.status === 'closed';
  // UI state is kept separate from the server's confirmed consultation summary.
  let waitStarted = 0, waitInterval = null, messageController = null;
  let recoveryTimer = null, recovering = false, afterWait = null, recoveryExpanded = false;
  let retryRequest = null;
  let sessionRevision = 0;
  let taskSnapshot = {}, taskSession = null, taskExpanded = new Set(), taskOrder = {};
  let planSession = null, planDone = {}, currentPlan = null, continuedAnswer = '';
  const scenarioPrompts = {cost:'У нас выросли счета за электроэнергию. С чего начать?',meter:'Хочу наладить учёт электроэнергии. С чего начать?',loss:'Хочу понять, где возникают потери электроэнергии. С чего начать?'};
  const illustrationPaths = {
    cost:'<path d="M26 17h36v58l-6-4-6 4-6-4-6 4-6-4-6 4V17Z"/><path d="M35 30h18M35 39h14M35 48h10"/><path class="et-art-accent" d="M35 59h18M73 63l12-13 10 7 14-23M98 34h11v11"/>',
    meter:'<rect x="36" y="16" width="56" height="62" rx="12"/><path d="M46 78v9M82 78v9"/><rect x="45" y="28" width="38" height="15" rx="3"/><path d="M56 28v15M68 28v15"/><path class="et-art-accent" d="M78 31v9" stroke-width="7"/><circle cx="64" cy="60" r="10"/><path d="M64 60l4-5"/>',
    loss:'<circle cx="25" cy="43" r="7"/><circle cx="103" cy="43" r="7"/><path d="M32 43h15M81 43h15"/><circle cx="64" cy="43" r="17"/><path d="m76 55 15 17M55 40h6M68 40h5"/><path class="et-art-accent" d="M59 50v4M65 52v5M71 49v5"/>',
    specialist:'<path d="M38 18h29l17 17v45H38V18Z"/><path d="M67 18v18h17M48 47h25M48 56h21M48 65h13"/><circle class="et-art-paper" cx="83" cy="72" r="13"/><path class="et-art-accent" d="m77 72 4 4 8-9"/>',
    conversation:'<path d="M23 24h51a8 8 0 0 1 8 8v23a8 8 0 0 1-8 8H49L35 75V63H23a8 8 0 0 1-8-8V32a8 8 0 0 1 8-8Z"/><path d="M30 38h37M30 48h23M90 45h10a8 8 0 0 1 8 8v22a8 8 0 0 1-8 8h-5v10L80 83H66a8 8 0 0 1-8-8"/><path class="et-art-accent" d="M76 70h.01M86 70h.01M96 70h.01" stroke-width="3"/>'
  };
  const illustration = name => `<span class="et-illustration" aria-hidden="true"><svg viewBox="0 0 128 100" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${illustrationPaths[name]}</svg></span>`;
  function renderScenarios() {
    const items=[['cost','Снизить расходы','Счета растут или кажутся завышенными'],['meter','Наладить учёт','Понять, где и сколько расходуется'],['loss','Найти потери','Выяснить, куда уходит электроэнергия']];
    $('.et-suggestions').innerHTML=items.map(([key,title,description])=>`<button type="button" class="et-scenario" data-prompt="${key}" aria-label="${title}" aria-pressed="false">${illustration(key)}<span class="et-scenario-title">${title}</span><span class="et-scenario-description">${description}</span><span class="et-scenario-action">Подставить вопрос <span aria-hidden="true">↘</span></span></button>`).join('');
  }
  function updateScenarioSelection() {
    host.querySelectorAll('[data-prompt]').forEach(el=>{
      const selected=input.value.trim()===scenarioPrompts[el.dataset.prompt];
      el.setAttribute('aria-pressed',String(selected));
      const label=el.querySelector('.et-scenario-action');
      if(label)label.innerHTML=selected?'✓ Вопрос подставлен':'Подставить вопрос <span aria-hidden="true">↘</span>';
    });
  }
  function nextStepsHTML() {
    return `<section class="et-next"><h2>Что дальше</h2><div class="et-next-grid"><article class="et-next-card">${illustration('specialist')}<div><h3>Передать задачу специалисту</h3><p>Соберём обращение из нашей беседы. Вы проверите его перед сохранением.</p>${button('Подготовить заявку','contacts',true)}</div></article><article class="et-next-card">${illustration('conversation')}<div><h3>Продолжить консультацию</h3><p>Задайте следующий вопрос. Контакты для этого не нужны.</p>${button('Продолжить консультацию','continue')}</div></article></div></section>`;
  }
  const draftKey = id => 'energytool-composer-' + id;
  const requestKey = id => 'energytool-pending-' + id;
  const formKey = id => 'energytool-application-draft-' + id;
  const retryKey = id => 'energytool-retry-' + id;
  function persistRetry() {
    if (!current) return;
    try { retryRequest ? sessionStorage.setItem(retryKey(current.id),JSON.stringify(retryRequest)) : sessionStorage.removeItem(retryKey(current.id)); } catch (_) {}
  }
  function persistForm() {
    if (!current) return;
    try { sessionStorage.setItem(formKey(current.id),JSON.stringify({draft,stage,noPhone})); } catch (_) {}
  }
  function persistRequest() {
    if (!current) return;
    try { unconfirmedMessage ? sessionStorage.setItem(requestKey(current.id),JSON.stringify(unconfirmedMessage)) : sessionStorage.removeItem(requestKey(current.id)); } catch (_) {}
  }
  function persistComposer() {
    if (!current) return;
    try { input.value ? sessionStorage.setItem(draftKey(current.id), input.value) : sessionStorage.removeItem(draftKey(current.id)); } catch (_) {}
  }
  function summaryValues(summary, key) {
    return [...new Set((Array.isArray(summary?.[key]) ? summary[key] : []).map(v => {
      if (typeof v === 'string') return v.trim();
      if (!v || typeof v !== 'object') return '';
      const text = v.value || v.text || v.quote || v.name;
      if (typeof text !== 'string') return '';
      const assumption = v.status === 'assumption' || v.confirmed === false;
      return (assumption ? 'Предположение: ' : '') + text.trim();
    }).filter(Boolean))];
  }
  // These labels are written by our field editor. Preserve that explicit schema;
  // do not ask a model to infer again which field the user just edited.
  function parseTaskMessage(text) {
    const fields = {'объект':'object','задача':'task','известные сведения':'parameters','условия':'constraints'};
    const pattern = /^(?:(Уточнение|Дополнение)\s*[—–-]\s*(объект|задача|известные сведения|условия)|Дополнение к задаче):[ \t]*/gmi;
    const matches = [...String(text).matchAll(pattern)];
    return {
      remainder: matches.length ? text.slice(0,matches[0].index).trim() : text.trim(),
      entries: matches.map((m,i) => ({
        key: fields[m[2]?.toLowerCase()] || 'notes',
        replace: m[1]?.toLowerCase() === 'уточнение',
        value: text.slice(m.index+m[0].length,matches[i+1]?.index ?? text.length).trim(),
      })).filter(e=>e.value),
    };
  }
  const evidenceText = text => String(text).toLowerCase().replace(/ё/g,'е').replace(/\s+/g,' ').trim();
  function taskData() {
    const history = current?.messages || [];
    const summaries = [current?.last_result?.summary, ...history.slice().reverse().map(m => m.response?.summary)];
    const summary = summaries.find(s => s && ['task','object','parameters','constraints','products','manager_questions'].some(k => summaryValues(s,k).length)) || {};
    const typed = [];
    for (let index=0;index<history.length;index++) {
      if (history[index].role === 'user') typed.push(...parseTaskMessage(history[index].content).entries.map(e=>({...e,index,unconfirmed:false})));
    }
    if (unconfirmedMessage && !unconfirmedMessage.retry) typed.push(...parseTaskMessage(unconfirmedMessage.text).entries.map(e=>({...e,index:unconfirmedMessage.offset,unconfirmed:true})));
    const firstQuestion = history.find(m=>m.role==='user')?.content || unconfirmedMessage?.text || '';
    const plainQuestion = parseTaskMessage(firstQuestion).remainder;
    const unique = values => [...new Set(values.filter(Boolean))];
    function modelEntries(key) {
      return (Array.isArray(summary[key]) ? summary[key] : []).flatMap(entry => {
        const text=typeof entry==='string'?entry:entry?.value || entry?.text || entry?.quote || '';
        const blocks=parseTaskMessage(text).entries;
        return blocks.length ? blocks.filter(b=>b.key===key).map(b=>({...entry,value:b.value})) : [entry];
      }).filter(entry => {
        const value = typeof entry === 'string' ? entry : entry?.value || entry?.text || entry?.quote || '';
        if (typeof value !== 'string' || parseTaskMessage(value).entries.length) return false;
        const index = entry?.message_index;
        if (!Number.isInteger(index) || history[index]?.role !== 'user') return true;
        const blocks = parseTaskMessage(history[index].content).entries;
        const owners = blocks.filter(block=>evidenceText(block.value).includes(evidenceText(value)));
        // A quote from the explicitly labelled object must not become the task.
        return !owners.length || owners.some(block=>block.key===key);
      });
    }
    function rowValues(key) {
      const entries = modelEntries(key);
      const serverValues = summaryValues({[key]:entries},key);
      if(summary.state_version===2){
        let values=serverValues;
        const pendingEdits=typed.filter(e=>e.key===key && e.unconfirmed);
        for(const edit of pendingEdits)values=edit.replace || ['object','task'].includes(key)?[edit.value]:unique([...values,edit.value]);
        return {values,meta:pendingEdits.length?'Вы указали · жду ответ':'Из подтверждённых сведений'};
      }
      const edits = typed.filter(e=>e.key===key);
      if (!edits.length) return {values:serverValues,meta:'Из резюме консультации'};
      const latest = edits.at(-1);
      const newer = entries.filter(e => Number.isInteger(e?.message_index) && e.message_index > latest.index &&
        history[e.message_index]?.role === 'user' && typeof e.value === 'string' &&
        evidenceText(history[e.message_index].content).includes(evidenceText(e.value)));
      if (newer.length && !latest.unconfirmed && ['object','task'].includes(key)) return {values:summaryValues({[key]:newer},key),meta:'Из последующих сообщений'};
      let values = edits[0].replace || ['object','task'].includes(key) ? [] : serverValues;
      for (const edit of edits) values = edit.replace || ['object','task'].includes(key) ? [edit.value] : unique([...values,edit.value]);
      if(newer.length && !latest.unconfirmed)values=unique([...values,...summaryValues({[key]:newer},key)]);
      return {values,meta:latest.unconfirmed ? (needsSync ? 'Вы указали · отправка не подтверждена' : 'Вы указали · жду ответ') : 'Вы указали в сообщении'};
    }
    const task = rowValues('task');
    if (!task.values.length && plainQuestion) {task.values=[plainQuestion];task.meta='Ваш вопрос дословно';}
    const questions = summaryValues(summary,'manager_questions');
    if (summary.state_version!==2 && !questions.length && !pending && !needsSync && current?.last_result?.status === 'clarification') {
      const answer = current.last_result.answer || history.at(-1)?.content;
      if (answer) questions.push(answer);
    }
    return [
      {key:'object',label:'Объект',...rowValues('object'),empty:'Пока не уточнили'},
      {key:'task',label:'Задача',...task,empty:'Что хотите решить?'},
      {key:'parameters',label:'Что известно',...rowValues('parameters'),empty:firstQuestion ? 'Сведения пока не выделены' : 'Факты появятся по ходу разговора'},
      {key:'questions',label:'Что уточнить',values:questions,empty:pending ? 'Появится после ответа' : 'Открытых вопросов пока нет',meta:'Вопросы консультанта'},
      {key:'constraints',label:'Условия',...rowValues('constraints')},
      {key:'notes',label:'Дополнения',...rowValues('notes')},
      {key:'products',label:'Обсуждаемые решения',values:summaryValues(summary,'products'),meta:'Выбор нужно подтвердить'},
    ].filter(row=>row.empty || row.values.length);
  }
  const taskIcons = {"building": "<path d=\"M4 16.5V5.5l6-2.5v13.5M10 7.5l6 2v7M2.75 16.5h14.5\"/><path d=\"M7 7.6v.01M7 10.6v.01M7 13.6v.01M13 11.6v.01M13 14.1v.01\" stroke-width=\"1.8\"/>", "target": "<circle cx=\"10\" cy=\"10\" r=\"7\"/><circle cx=\"10\" cy=\"10\" r=\"3.2\"/><path d=\"M10 9.99v.02\" stroke-width=\"2\"/>", "checkCircle": "<circle cx=\"10\" cy=\"10\" r=\"7\"/><path d=\"M6.8 10.2l2.2 2.2 4.2-4.6\"/>", "question": "<circle cx=\"10\" cy=\"10\" r=\"7\"/><path d=\"M8 8.1a2 2 0 113.1 1.7c-.6.4-1.1.8-1.1 1.6\"/><path d=\"M10 13.9v.01\" stroke-width=\"2\"/>", "list": "<path d=\"M7.5 5.5H16M7.5 10H16M7.5 14.5H16\"/><path d=\"M4 5.5h.01M4 10h.01M4 14.5h.01\" stroke-width=\"2\"/>", "layers": "<path d=\"M10 3l7 3.800-7 3.800-7-3.800L10 3z\"/><path d=\"M3.500 10.500l6.500 3.500 6.500-3.500M3.500 13.800L10 17.300l6.500-3.500\"/>", "edit": "<path d=\"M4 16l.8-3.4 8.8-8.8a1.4 1.4 0 012 0l.6.6a1.4 1.4 0 010 2l-8.8 8.8L4 16z\"/><path d=\"M12.2 5.2l2.6 2.6\"/>"};
  const taskIcon = name => `<svg width="18" height="18" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${taskIcons[name]}</svg>`;
  function taskRowsHTML(rows, animate = false, full = false) {
    return rows.map(row => {
      const changed = animate && row.values.length && taskSnapshot[row.key] !== JSON.stringify(row.values);
      const values=taskOrder[row.key] || row.values;
      const expanded=full || taskExpanded.has(row.key), overflow=values.length>2 || values.some(v=>v.length>180);
      const shown=expanded?values:values.slice(0,2), id=`et-task-${full?'all':'short'}-${row.key}`;
      const disclosure=!full && overflow ? `<button type="button" class="et-text-button et-task-more" data-task-expand="${row.key}" aria-expanded="${expanded}" aria-controls="${id}">${expanded?'Свернуть':values.length>2?`Показать ещё ${values.length-2}`:'Показать полностью'} <span aria-hidden="true">${expanded?'⌃':'⌄'}</span></button>` : '';
      return `<section class="et-task-row${!values.length ? ' empty' : ''}${changed ? ' et-task-new' : ''}" data-task-key="${row.key}"><span class="et-task-icon" aria-hidden="true">${taskIcon(({object:'building',task:'target',parameters:'checkCircle',questions:'question',constraints:'list',notes:'list',products:'layers'})[row.key])}</span><div class="et-task-content"><div class="et-task-label">${row.label}${values.length>1?` · ${values.length}`:''}</div><div id="${id}" class="et-task-value${!expanded&&overflow?' et-task-clamped':''}">${values.length ? (values.length === 1 ? `<span>${escape(shown[0])}</span>` : `<ul class="et-task-list">${shown.map(v=>`<li>${escape(v)}</li>`).join('')}</ul>`) : `<span class="et-muted">${row.empty}</span>`}</div>${disclosure}${values.length ? `<div class="et-task-meta">${row.meta}</div>` : ''}</div>${['object','task','parameters','constraints'].includes(row.key) && current && !isClosed() ? `<button type="button" class="et-task-edit" data-task-field="${row.key}" aria-label="Уточнить: ${row.label}">${taskIcon('edit')}</button>` : ''}</section>`;
    }).join('');
  }
  function renderTask() {
    if (taskSession !== current?.id) { taskSession = current?.id; taskSnapshot = {}; taskExpanded=new Set();taskOrder={}; }
    const rows = taskData();
    rows.forEach(row=>{
      const old=taskOrder[row.key] || [];
      taskOrder[row.key]=[...row.values.filter(v=>!old.includes(v)),...old.filter(v=>row.values.includes(v))];
    });
    const text = pending ? 'Жду ответ. Можно подготовить дополнения' : needsSync ? 'Проверяю готовность ответа' : current?.messages.length ? 'Собрано по ходу консультации' : 'Заполняется по ходу разговора';
    host.querySelectorAll('.et-task-status').forEach(n => { n.textContent = text; });
    host.querySelectorAll('.et-task-rows').forEach(n => { n.innerHTML = taskRowsHTML(rows, !pending && !needsSync,Boolean(n.closest('dialog'))); });
    taskSnapshot = Object.fromEntries(rows.map(row => [row.key,JSON.stringify(row.values)]));
    const mobile = $('.et-task-mobile-label');
    if (mobile) mobile.textContent = rows[0].values[0] ? `Ваша задача · ${rows[0].values[0]}` : 'Ваша задача в фокусе';
  }
  function openTask() {
    openModal('Ваша задача в фокусе',`<p class="et-task-status"></p><div class="et-task-rows"></div><div class="et-stack">${button('Добавить факт','task-add')}</div>`,'task drawer');
    renderTask(); controls();
  }
  // Only copy explicit actions from a saved answer. A question or model waiting
  // state must never fabricate a plan or mark the user's work complete.
  function planFromConversation() {
    const history=current?.messages || [];
    for(let i=history.length-1;i>=0;i--){
      const message=history[i];
      if(message.role!=='assistant' || !['answered','clarification'].includes(metadata(message,i)?.status))continue;
      const summary=message.response?.summary || (i===history.length-1?current?.last_result?.summary:null);
      const directQuestion=/^(?:уточните|сообщите|скажите|подскажите)\s*[,—:]?\s*(?:есть\s+ли\s+у\s+вас|имеются\s+ли\s+у\s+вас|как\s+вы|(?:како[йев]|какая|сколько)\s+у\s+вас)(?=\s|[.,:;!]|$)/iu;
      const supplied=Array.isArray(summary?.action_plan)?summary.action_plan.filter(s=>typeof s==='string' && s.length>=8 && s.length<=650 && !s.includes('?') && !directQuestion.test(s) && evidenceText(message.content.replace(/\*\*/g,'')).includes(evidenceText(s))):[];
      if(supplied.length)return {index:i,steps:[...new Set(supplied)].slice(0,8)};
      if(summary?.state_version===2)continue;
      const action=/^(?:(?:сначала|затем|далее|также|после этого|для начала)\s*[,—:]?\s*)?(?:можно\s+)?(?:начн|сравн|сопостав|провер|собер|собрат|уточн|выясн|обсуд|запиш|посмотр|подготов|измер|определ|свяж|обрат|учт|попрос|проанализ|рассмотр|оцен|провед|провест|зафикс|налад|внедр)[а-яё]*(?=\s|[.,:;!]|$)/iu;
      const steps=String(message.content).split(/\n+|(?=\b\d{1,2}[.)]\s)|(?<=[.!?])\s+/).map(s=>s.replace(/^\s*(?:[-*•]|\d+[.)])\s*/,'').replace(/\*\*/g,'').trim().replace(/;$/,'')).filter(s=>
        s.length>=8 && s.length<=650 && !s.includes('?') && !directQuestion.test(s) && (action.test(s) || (/^если\s/iu.test(s) && s.split(',').slice(1).some(clause=>action.test(clause.trim())))));
      if(steps.length)return {index:i,steps:[...new Set(steps)].slice(0,8)};
    }
    return null;
  }
  function planHTML(plan) {
    if(!plan)return '<p class="et-hint">Шаги появятся после ответа с рекомендациями.</p>';
    const done=plan.steps.filter(s=>planDone[s]===true).length;
    const firstOpen=plan.steps.findIndex(s=>!planDone[s]);
    return `<div class="et-plan-heading"><h2>План действий</h2><span class="et-plan-count">${done} из ${plan.steps.length}</span></div><p class="et-hint et-plan-caption">Из рекомендаций консультанта</p><ol class="et-plan-list">${plan.steps.map((text,i)=>`<li class="${planDone[text]?'is-done':i===firstOpen?'is-current':''}"><span class="et-plan-number" aria-hidden="true">${planDone[text]?'✓':i+1}</span><label><span>${escape(text)}</span><input type="checkbox" data-plan-step="${i}" aria-label="Выполнено: ${escape(text)}" ${planDone[text]?'checked':''}></label>${i===firstOpen?'<small>Следующий шаг</small>':''}</li>`).join('')}</ol><button type="button" class="et-text-button" data-action="plan-source">Посмотреть ответ ↗</button><p class="et-hint et-plan-note">Отмечайте выполненное. Отметки сохраняются в этом браузере.</p>`;
  }
  function renderPlan() {
    if(planSession!==current?.id){
      planSession=current?.id;planDone={};
      try{const stored=JSON.parse(localStorage.getItem('energytool-plan-'+planSession)||'{}');if(stored && typeof stored==='object' && !Array.isArray(stored))planDone=stored;}catch(_){}
    }
    currentPlan=planFromConversation();
    const visible=Boolean(currentPlan) && stage==='chat';
    host.classList.toggle('et-has-plan',visible);
    $('.et-plan-sidebar').hidden=!visible;
    $('.et-plan-toggle').hidden=!visible;
    host.querySelectorAll('.et-plan-content').forEach(n=>{n.innerHTML=planHTML(currentPlan);});
  }
  function editTask(key = '') {
    const names = {object:'объект',task:'задача',parameters:'известные сведения',constraints:'условия'};
    const value = taskData().find(r=>r.key===key)?.values.join('\n') || '';
    openModal(key ? 'Уточнить задачу' : 'Добавить факт',`<p class="et-hint">Добавление попадёт в черновик следующего сообщения. Уже отправленный вопрос и сохранённые данные не изменятся до отправки и ответа.</p><form class="et-task-form">${key ? '' : '<label class="et-field"><span>Куда добавить</span><select name="field" aria-label="Куда добавить"><option value="parameters">Что известно</option><option value="object">Объект</option><option value="task">Задача</option><option value="constraints">Условия и пожелания</option></select></label>'}<label class="et-field"><span>Дополнение к задаче</span><textarea name="detail" aria-label="Дополнение к задаче" maxlength="3000" required>${escape(value)}</textarea></label><p class="et-hint" role="status"></p><div class="et-stack"><button type="submit" class="et-primary">Добавить в черновик</button>${button('Отмена','dismiss')}</div></form>`,'task-edit');
    const form = modalBody.querySelector('form');
    form.onsubmit = event => {
      event.preventDefault();
      const value = form.elements.detail.value.trim();
      if (!value) return;
      const field = key || form.elements.field.value;
      const addition = `${key ? 'Уточнение' : 'Дополнение'} — ${names[field]}: ` + value;
      const next = [input.value.trim(),addition].filter(Boolean).join('\n\n');
      if (next.length > input.maxLength) { form.querySelector('[role="status"]').textContent = 'В черновике слишком много текста. Сократите дополнение.'; return; }
      setInput(next); dismiss(); focusComposer();
    };
    form.elements.detail.focus();
  }
  function focusComposer() {
    input.focus({preventScroll:true});
    $('.et-composer').scrollIntoView({block:'nearest',behavior:'auto'});
  }
  function waitingHTML() {
    return `<section class="et-pending" role="status"><div class="et-pending-head"><span>EnergyTool · ИИ-консультант</span><span class="et-wait-timer" aria-live="off">Жду ответ 0:00</span></div><div class="et-wait-title"><span class="et-energy-bars" aria-hidden="true"><i></i><i></i><i></i></span><span class="et-wait-caption">Готовлю ответ…</span></div><p class="et-hint">Можно подготовить следующий вопрос или вернуться к диалогу позже.</p><div class="et-wait-long" hidden><p class="et-hint">Ответ занимает больше времени. Можно оставить заявку оператору — задача и переписка сохранятся.</p>${button('Позвать оператора','operator',true)}</div><div class="et-stack">${button('Прервать ожидание','stop-wait')}</div></section>`;
  }
  function recoveryHTML() {
    return card(recoveryExpanded ? 'Проверяю готовность ответа' : 'Ожидание прервано',
      '<p>Вопрос сохранён в этом браузере. Проверяю историю автоматически: готовый ответ появится здесь. Можно подготовить черновик, оставить заявку оператору или вернуться позже через историю диалогов.</p><p class="et-hint">Повторно вопрос не отправляется. Обращение оператору сохранится в очереди заявок; мгновенный ответ не гарантируется.</p>',
      button('Обновить историю','refresh') + button('Позвать оператора','operator',true) + (recoveryExpanded ? '' : button('Продолжить ожидание','resume-wait')),
      'et-recovery');
  }
  function pauseWait(action) {
    if (pending) { afterWait = action; messageController?.abort(); }
    else action();
  }
  // Only read while reconciling an interrupted request. Never silently repeat a POST.
  function scheduleRecovery() {
    clearTimeout(recoveryTimer);
    if (!needsSync || !current || pending) return;
    recoveryTimer = setTimeout(checkRecovery,3000);
  }
  async function checkRecovery() {
    if (!needsSync || !current || pending) return;
    if (busy || recovering) { scheduleRecovery(); return; }
    const id = current.id;
    const revision = sessionRevision;
    recovering = true;
    try {
      const session = await api('/sessions/'+id);
      if (current?.id !== id || pending || busy || revision !== sessionRevision) return;
      const changed = JSON.stringify(current) !== JSON.stringify(session);
      adopt(session);
      if (!needsSync) {
        recoveryExpanded = false;
        status(stage === 'chat' ? '' : 'Ответ получен. Можно вернуться к консультации.');
      }
      // A poll must not replace a form while the user is filling it in.
      if (changed || !needsSync) {
        if (stage === 'chat') render(); else { renderTask(); controls(); }
      }
    } catch (_) {
      if (current?.id === id && needsSync && !busy && revision === sessionRevision) status('Не удалось обновить историю. Проверю ещё раз автоматически. Можно оставить заявку или вернуться позже.');
    } finally { recovering = false; scheduleRecovery(); }
  }
  function updateWaiting() {
    const help = $('.et-wait-help');
    if (!pending) { if (help) help.hidden = true; clearInterval(waitInterval); waitInterval = null; return; }
    const seconds = Math.floor((performance.now()-waitStarted)/1000);
    const timer = $('.et-wait-timer');
    if (timer) timer.textContent = `Жду ответ ${Math.floor(seconds/60)}:${String(seconds%60).padStart(2,'0')}`;
    if (help) help.hidden = seconds < 4 || stage !== 'chat';
    const long = $('.et-wait-long');
    if (long) long.hidden = seconds < 20;
    if (seconds >= 20 && $('.et-wait-caption')) $('.et-wait-caption').textContent = 'Всё ещё готовлю ответ';
  }

  function status(text, retry = false) {
    notice = text;
    $('.et-status').hidden = !text;
    $('.et-status').innerHTML = escape(text) + (retry ? button('Обновить историю', 'refresh') : '');
  }
  function controls() {
    const safeActions = ['dismiss','task','task-all','task-add','plan','stop-wait',...(pending ? ['operator','contacts','menu','history','new','close','site'] : [])];
    host.querySelectorAll('[data-action], [data-prompt], [data-session], [data-reply], [data-source-message], [data-fragment], .et-flow input, .et-flow textarea, .et-contact-form button, .et-edit-form input, .et-edit-form textarea, .et-edit-form button').forEach(el => {
      el.disabled = busy && !safeActions.includes(el.dataset.action) && !el.hasAttribute('data-source-message') && !el.hasAttribute('data-fragment');
    });
    input.disabled = (busy && !pending) || !current || isClosed();
    $('.et-send').disabled = busy || needsSync || input.disabled || !input.value.trim();
    $('.et-send').textContent = 'Отправить';
    input.placeholder = pending || needsSync ? 'Можно писать следующее сообщение' : 'Опишите вашу задачу';
    $('.et-compose-actions>span').textContent = pending || needsSync ? 'Отправка откроется после проверки ответа. Черновик сохранится.' : 'Enter — отправить · Shift+Enter — новая строка';
    host.querySelectorAll('[data-task-field], [data-task-suggest], [data-action="task-add"]').forEach(el => { el.disabled = !current || isClosed() || (busy && !pending); });
    host.querySelectorAll('[data-reply]').forEach(el => { el.disabled = busy || needsSync || isClosed(); });
    $('.et-composer').hidden = stage !== 'chat';
    updateScenarioSelection();
  }
  async function api(path, body, signal) {
    const response = await fetch('/api/v1' + path, {method:body === undefined ? 'GET':'POST',
      headers:body == null ? {} : {'Content-Type':'application/json'},
      body:body == null ? undefined:JSON.stringify(body), signal:signal ? AbortSignal.any([AbortSignal.timeout(275000),signal]) : AbortSignal.timeout(20000)});
    let data; try { data = await response.json(); } catch (_) { throw new Error('Сервис вернул некорректный ответ.'); }
    if (!response.ok) {
      const labels = {not_found:'Диалог не найден.',session_closed:'Диалог завершён. Начните новый.', history_limit:'Достигнут лимит диалога. Начните новый — история сохранена.', invalid_application:'Проверьте телефон или email и описание задачи.', application_closed:'Менеджер уже обработал заявку: редактирование закрыто. Дополнения можно написать в консультации — они сохранятся в истории.', message_too_long:'Сообщение слишком длинное.',empty_message:'Введите вопрос.'};
      const error = new Error(labels[data.code] || data.message || 'Сервис временно недоступен.'); error.code = data.code; throw error;
    }
    return data;
  }
  async function run(fn) {
    if (busy) return;
    busy = true; controls();
    try { await fn(); } catch (error) { status(error.name === 'TimeoutError' ? 'Ответ занимает больше времени. Обновите историю перед повторной отправкой.' : error.message || 'Не удалось подключиться к сервису.', true); if (modal.open) { let alert = modalBody.querySelector('.et-modal-error'); if (!alert) { alert = document.createElement('p'); alert.className = 'et-error et-modal-error'; alert.setAttribute('role','alert'); modalBody.append(alert); } alert.textContent = notice; alert.scrollIntoView({block:'nearest'}); } }
    finally { busy = false; controls(); scheduleRecovery(); }
  }
  function adopt(session) {
    const previousID = current?.id;
    sessionRevision++;
    current = session; remember(session.id);
    if (previousID !== session.id) {
      unconfirmedMessage = null; retryRequest = null; recoveryExpanded = false;
      draft = null; stage = 'chat'; noPhone = false; collapsedAt = 0;
      try {
        const savedForm = JSON.parse(sessionStorage.getItem(formKey(session.id)) || 'null');
        if (savedForm?.draft && typeof savedForm.draft === 'object') draft = savedForm.draft;
        if (savedForm && ['chat','contacts','review','declined','saved'].includes(savedForm.stage) &&
            (savedForm.stage !== 'saved' || session.application) && (!['contacts','review'].includes(savedForm.stage) || draft)) stage = savedForm.stage;
        noPhone = Boolean(savedForm?.noPhone);
        const savedRetry = JSON.parse(sessionStorage.getItem(retryKey(session.id)) || 'null');
        if (savedRetry?.sessionID === session.id && typeof savedRetry.text === 'string') retryRequest = savedRetry;
      } catch (_) {}
      if (session.status === 'closed') stage = 'chat';
      let savedDraft = ''; try { savedDraft = sessionStorage.getItem(draftKey(session.id)) || ''; } catch (_) {}
      setInput(savedDraft);
      if (!unconfirmedMessage) {
        try {
          const saved = JSON.parse(sessionStorage.getItem(requestKey(session.id)) || 'null');
          if (saved?.sessionID === session.id && typeof saved.text === 'string' && Number.isInteger(saved.offset)) unconfirmedMessage = saved;
        } catch (_) {}
      }
    }
    if (unconfirmedMessage?.sessionID === session.id) {
      const { offset, text, restored, retry, requestID } = unconfirmedMessage;
      const record = requestID && session.requests?.[requestID];
      const recorded = Boolean(record || session.messages.some(message=>requestID && message.response?.request_id===requestID) ||
        session.messages.slice(offset).some(message => message.role === 'user' && message.content === text));
      if (recorded && restored && input.value.trim() === text) setInput('');
      if (!recorded && !retry && input.value.trim() !== text) {
        setInput([text,input.value.trim()].filter(Boolean).join('\n\n'));
      }
      if (!recorded) retryRequest = {...unconfirmedMessage};
      else retryRequest = null;
      persistRetry();
      unconfirmedMessage = null;persistRequest();
    }
    needsSync = session.status === 'active' && (session.messages.at(-1)?.role === 'user' || Object.values(session.requests || {}).some(request=>request.pending));
    if (needsSync) {
      if (previousID !== session.id) recoveryExpanded = true;
      status('');
    }
    scheduleRecovery();
  }
  function metadata(message, index, session = current) {
    const stored = message.response;
    const last = message.role === 'assistant' && index === session?.messages.length - 1 ? session.last_result : null;
    if (!stored) return last;
    // Older backends retained debug only in last_result. Recover that field only
    // for a proven matching last answer; its saved timing/citations stay intact.
    if (stored.debug == null && last?.debug && typeof last.debug === 'object' && !Array.isArray(last.debug) &&
        last.answer === message.content && last.status === stored.status) {
      return {...stored, debug:last.debug};
    }
    return stored;
  }
  function isRetriedQuestion(message, index, session) {
    return message.role === 'user' && index > 1 &&
      session.messages[index - 2].role === 'user' &&
      metadata(session.messages[index - 1], index - 1, session)?.status === 'technical_error' &&
      session.messages[index - 2].content === message.content;
  }
  function messageHTML(message, animate = false) {
    return `<article class="et-message ${message.role === 'user' ? 'et-user' : 'et-assistant'}${animate ? ' et-answer-new' : ''}">${message.role === 'user' ? '' : `<div class="et-message-label">${message.response?.origin === 'specialist' ? 'Специалист EnergyTool' : 'EnergyTool · ИИ-консультант'}</div>`}<div class="et-message-body">${escape(message.content)}</div></article>`;
  }
  function responseTime(result) {
    return Number.isFinite(result?.elapsed_ms) && result.elapsed_ms >= 0
      ? (result.elapsed_ms / 1000).toLocaleString('ru-RU', { maximumFractionDigits: 1 }) + ' с'
      : 'Не указано';
  }
  const citedSources = result => Array.isArray(result?.sources) ? result.sources.filter(source => source && typeof source === 'object') : [];
  function debugCard(result, index) {
    return `<section class="et-card et-debug"><p class="et-hint">Отладка · ${responseLabels.get(result.status) || 'Статус неизвестен'}</p><div class="et-stack"><button data-source-message="${index}">${responseTime(result)} · Цитаты: ${citedSources(result).length} · Подробнее</button>${button('Выключить отладку','debug')}</div></section>`;
  }
  function chatMessages() {
    return (current?.messages || []).slice(collapsedAt).map((m, offset) => {
      const index = offset + collapsedAt, result = metadata(m,index);
      if (isRetriedQuestion(m, index, current)) return '';
      if (m.role === 'assistant' && result?.status === 'technical_error') {
        const hint = index === current.messages.length - 1 ? '' : '<p class="et-hint">Не удалось получить ответ. Вопрос сохранён.</p>';
        return hint + (debug ? `<button class="et-debug-link" data-source-message="${index}">Данные о сбое</button>` : '');
      }
      const key = `${current.id}:${index}:${m.content}`;
      const animate = m.role === 'assistant' && index === current.messages.length-1 && key !== lastShownAnswer;
      if (m.role === 'assistant' && index === current.messages.length-1) lastShownAnswer = key;
      return messageHTML(m,animate) + (m.role === 'assistant' && debug && result ? (index === current.messages.length-1 ? debugCard(result,index) : `<button class="et-debug-link" data-source-message="${index}">Источники этого ответа</button>`) : '');
    }).join('');
  }
  function clarificationChoices(answer) {
    let choices = [];
    // Product explanations and acknowledgements are not questions. In
    // particular, “вы видите только общую сумму” must not ask for that again.
    const questionText=(String(answer).match(/[^.!?\n]*\?/g)||[]).join(' ');
    const known=summaryValues(current?.last_result?.summary || {},'parameters').join(' ');
    const knownLayout=/(?:общ[а-яё]*\s+(?:сч[её]тчик|сумм|сч[её]т)|отдельн[а-яё]*\s+сч[её]тчик|показания\s+по\s+(?:кажд|отдельн))/iu.test(known);
    const knownMeters=knownLayout || /(?:есть|нет|имеются|установлен)[^.!?]*(?:прибор[а-яё]*\s+уч[её]та|сч[её]тчик)/iu.test(known);
    if (!knownLayout && /показани|сч[её]тчик/i.test(questionText) && /общ|кажд|отдельн/i.test(questionText)) choices = ['Вижу только общую сумму','Есть показания по каждому'];
    else if (!knownMeters && /есть ли.*прибор|есть.*прибор.*уч[её]т/i.test(questionText)) choices = ['Есть приборы учёта','Пока нет приборов учёта'];
    // A repeated model question must not encourage repeating the same shortcut.
    const latestUser = current?.messages.findLast(m=>m.role==='user')?.content || '';
    return choices.some(text=>evidenceText(text)===evidenceText(latestUser)) ? [] : choices;
  }
  function render() {
    persistForm();
    host.dataset.screen = stage === 'chat' ? 'conversation' : stage;
    const empty = !current?.messages.length;
    $('.et-welcome').hidden = !empty || stage !== 'chat' || pending || needsSync;
    messages.hidden = stage !== 'chat';
    messages.innerHTML = (collapsedAt && stage === 'chat' ? previous() : '') + chatMessages();
    if (unconfirmedMessage && !unconfirmedMessage.retry && (pending || needsSync)) messages.insertAdjacentHTML('beforeend',messageHTML({role:'user',content:unconfirmedMessage.text}));
    flow.replaceChildren();
    if (stage === 'contacts') renderContacts();
    else if (stage === 'review') renderReview(draft,false);
    else if (stage === 'saved') renderReview(current.application,true);
    else if (stage === 'declined') {
      flow.innerHTML = previous() + messageHTML({role:'user',content:'Продолжить без контакта'}) + assistant('Хорошо, можем продолжить без контакта. Хотите разобраться, какие данные собрать по вашей задаче?') + `<div class="et-stack">${button('Продолжить консультацию','continue',true)}<button data-reply="Какие данные нужно собрать по моей задаче?">Какие данные собрать?</button>${button('Вернуться к заявке','contacts')}</div>`;
    } else if (pending) {
      flow.innerHTML = waitingHTML();
    } else if (isClosed()) {
      flow.innerHTML = card('Диалог сохранён','<p>Можно продолжить эту переписку или начать новую.</p>',button('Продолжить этот диалог','resume',true)+button('Начать новый диалог','new'));
    } else if (needsSync) {
      flow.innerHTML = recoveryHTML();
    } else if (!needsSync) {
      const result = current?.last_result;
      const state = result?.status;
      host.dataset.screen = empty ? 'welcome' : state || 'conversation';
      if (state === 'technical_error') {
        flow.innerHTML = card('Не удалось получить ответ','<p>Ваш вопрос сохранён. Можно повторить попытку, задать другой вопрос или оставить заявку оператору.</p>',button('Повторить','retry',true)+button('Позвать оператора','operator')+button('Задать другой вопрос','continue'),'et-error');
      } else if (state === 'out_of_scope') {
        flow.innerHTML = `<div class="et-stack">${button('Задать другой вопрос','continue')}</div>`;
      } else if (noPhone && !current?.application) {
        flow.innerHTML = `<div class="et-stack">${button('Вернуться к заявке','contacts')}</div>`;
      } else if (state === 'knowledge_gap') {
        flow.innerHTML = `<div class="et-stack">${button('Подготовить заявку','contacts',true)}${button('Задать другой вопрос','continue')}</div>`;
      } else if (current?.lead?.requested && !current.lead.declined && !current.application) {
        flow.innerHTML = `<div class="et-stack">${button('Подготовить заявку','contacts',true)}${button('Продолжить консультацию','continue')}</div>`;
      } else if (state === 'clarification') {
        const choices = clarificationChoices(result.answer || current.messages.at(-1)?.content || '');
        flow.innerHTML = `<div class="et-stack et-replies">${choices.map((text,i)=>`<button class="${i===0?'et-primary':''}" data-reply="${escape(text)}">${escape(text)}</button>`).join('')}</div>`;
      } else if (state === 'answered' && !current?.application && continuedAnswer !== `${current.id}:${current.messages.length}`) {
        flow.innerHTML = nextStepsHTML();
      } else if (current?.application && !empty) {
        flow.innerHTML = `<div class="et-stack">${button('Сохранённая заявка','application')}</div>`;
      }
    }
    if (stage !== 'chat') host.dataset.screen = stage;
    if (pending) host.dataset.screen = 'waiting';
    if (needsSync && stage === 'chat') host.dataset.screen = 'sync';
    const receipt=$('.et-application-receipt');
    receipt.hidden=!current?.application;
    receipt.innerHTML=current?.application ? `<button type="button" data-action="application">Заявка № ${escape(current.application.number || current.application.id)}</button><span>Сохранена · можно открыть в любой момент</span>` : '';
    renderTask(); renderPlan(); updateWaiting(); controls();
  }
  function summaryText() {
    const labels = {task:'Задача клиента',object:'Объект',parameters:'Известные параметры',constraints:'Условия и ограничения',notes:'Дополнения клиента',products:'Обсуждавшиеся решения (выбор нужно подтвердить)',questions:'Уточнить с менеджером'};
    const rows = taskData();
    // No phantom summary for the untouched welcome screen.
    if (!current?.messages.length && !unconfirmedMessage) return '';
    const structured = rows.some(r=>['task','object','parameters','constraints','notes'].includes(r.key) && r.values.length && r.meta !== 'Ваш вопрос дословно');
    const fallback = !structured ? current.messages.filter(m=>m.role==='user' && m.content.trim()) : [];
    if (!structured && unconfirmedMessage && !fallback.some(m=>m.content===unconfirmedMessage.text)) fallback.push({content:unconfirmedMessage.text});
    const selected = fallback.length > 4 ? [fallback[0],...fallback.slice(-3)] : fallback;
    const parts = selected.length ? [`Из сообщений клиента${fallback.length>4?' (начало и последние уточнения)':''}:\n${selected.map(m=>'• '+m.content.trim()).join('\n')}`] : [];
    // While the next answer is pending, the previous model summary cannot yet
    // contain new free-form facts. Keep them as the client's words in the handoff.
    if (structured) {
      const history = current.messages;
      const coveredThrough = history.findLastIndex(message => message.role === 'assistant' &&
        message.response?.origin !== 'specialist' && message.response?.status !== 'technical_error');
      const uncovered = history.slice(coveredThrough+1).filter(message=>message.role==='user').map(message=>message.content);
      if (unconfirmedMessage && !unconfirmedMessage.retry) uncovered.push(unconfirmedMessage.text);
      const details = [...new Set(uncovered.map(text=>parseTaskMessage(text).remainder).filter(Boolean))];
      if (details.length) parts.push(`Последние уточнения клиента:\n${details.map(text=>'• '+text).join('\n')}`);
    }
    const text = [...parts,...(structured ? ['task','object','parameters','constraints','notes','products','questions'] : ['products','questions']).map(key=>{
      const row = rows.find(r=>r.key===key);
      return row?.values.length ? `${labels[key]}:\n${row.values.map(v=>'• '+v).join('\n')}` : '';
    })].filter(Boolean).join('\n\n');
    if (text.length <= 2000) return text;
    const note = '\n\n[Описание сокращено. Полные сообщения — в истории диалога.]';
    return text.slice(0,2000-note.length-1).trimEnd()+'…'+note;
  }
  function startContacts() {
    if (!current || isClosed()) return;
    const fields = current.lead?.fields || {};
    const generated = summaryText();
    if (!draft) {
      draft = current.application ? {...current.application,summary_source:'manual'} : {
        full_name:fields.full_name || '',phone:fields.phone || '',email:fields.email || '',company:fields.company || '',
        summary:generated,summary_source:generated ? 'conversation' : 'manual',
      };
    } else {
      // Legacy drafts have no provenance. Preserve their wording rather than
      // treating a potentially edited description as generated text.
      draft.summary_source ||= 'manual';
      if (draft.summary_source === 'conversation') draft.summary = generated;
      else if (!draft.summary?.trim() && generated) { draft.summary = generated; draft.summary_source = 'conversation'; }
    }
    draft.contact = draft.contact ?? (draft.phone || draft.email || '');
    stage = 'contacts'; render(); scroll.scrollTop = 0; flow.querySelector('input')?.focus();
  }
  function moscowParts(value = new Date()) {
    const parts = new Intl.DateTimeFormat('en-GB',{timeZone:'Europe/Moscow',year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hourCycle:'h23'}).formatToParts(value);
    const p = Object.fromEntries(parts.map(x=>[x.type,x.value]));
    return {date:`${p.year}-${p.month}-${p.day}`,time:`${p.hour}:${p.minute}`};
  }
  function callbackFields(value) {
    const mode=value.callback_mode || 'asap';
    const at=value.callback_at ? moscowParts(new Date(value.callback_at)) : {};
    return `<fieldset class="et-callback"><legend>Когда вам удобно поговорить?</legend><p class="et-callback-caption">Выберите удобный вариант связи по телефону.</p><div class="et-callback-options"><label class="et-call-choice"><input type="radio" name="callback_mode" value="asap" ${mode==='asap'?'checked':''}><span><strong>Как можно скорее</strong><small>Готов поговорить сейчас</small></span></label><label class="et-call-choice"><input type="radio" name="callback_mode" value="scheduled" ${mode==='scheduled'?'checked':''}><span><strong>В удобное время</strong><small>Выбрать дату и время</small></span></label></div><div class="et-callback-date" ${mode==='scheduled'?'':'hidden'}><label class="et-field"><span>Дата звонка</span><input type="date" name="callback_date" aria-label="Дата звонка" min="${moscowParts().date}" value="${escape(value.callback_date || at.date || '')}"></label><label class="et-field"><span>Время звонка · МСК</span><input type="time" name="callback_time" aria-label="Время звонка · МСК" value="${escape(value.callback_time || at.time || '')}"></label></div><p class="et-callback-note">${mode==='scheduled'?'Время московское (UTC+3). Сохраним пожелание; запись на звонок пока не подтверждена.':'Укажем, что вы готовы к звонку сейчас. Немедленное соединение с менеджером не гарантируется.'}</p></fieldset>`;
  }
  function callbackLabel(value) {
    if(value.callback_mode==='asap')return 'Как можно скорее — по возможности менеджера';
    if(value.callback_mode==='scheduled') {
      const raw=value.callback_date && value.callback_time ? `${value.callback_date}T${value.callback_time}:00+03:00` : value.callback_at;
      if(raw && !Number.isNaN(Date.parse(raw)))return new Intl.DateTimeFormat('ru-RU',{timeZone:'Europe/Moscow',dateStyle:'long',timeStyle:'short'}).format(new Date(raw))+' МСК — желаемое время';
    }
    return 'Не указано';
  }
  function validEmail(value) {
    if (value.length > 254 || value.split('@').length !== 2) return false;
    const [local, domain] = value.split('@');
    return local.length > 0 && local.length <= 64 && /^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+$/.test(local) &&
      !local.startsWith('.') && !local.endsWith('.') && !local.includes('..') &&
      domain.split('.').length >= 2 && domain.split('.').every(label => /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(label));
  }
  function contactValues(fields, previous = {}) {
    const contact = String(fields.contact ?? previous.phone ?? previous.email ?? '').trim();
    const emailOnly = contact.includes('@');
    return {...previous, ...fields, contact,
      phone: emailOnly ? '' : contact,
      email: emailOnly ? contact : (previous.phone ? previous.email || '' : ''),
      ...(emailOnly ? {callback_mode:'asap', callback_at:'', callback_date:'', callback_time:''} : {}),
    };
  }
  function syncContactMode(form) {
    const emailOnly = form.elements.contact.value.includes('@');
    const callbacks = form.querySelector('.et-callback');
    callbacks.hidden = emailOnly;
    callbacks.disabled = emailOnly;
    form.querySelector('.et-email-note').hidden = !emailOnly;
  }
  function contactFields(value, includeSummary = false) {
    value = {...value, contact:value.contact ?? (value.phone || value.email || '')};
    const field = (name,label,placeholder,extra='') => `<label class="et-field${name==='contact'?' et-field-wide':''}"><span>${label}</span><input aria-label="${label}" name="${name}" value="${escape(value[name])}" placeholder="${placeholder}" ${extra}></label>`;
    return `<div class="et-fields">${field('contact','Телефон или email','+7 (___) ___-__-__ или name@example.ru','type="text" inputmode="email" autocomplete="off" maxlength="254" autocapitalize="off" spellcheck="false" required')}${field('full_name','Имя и фамилия','Как к вам обращаться','autocomplete="name" maxlength="200"')}${field('company','Название компании','Ваша компания','autocomplete="organization" maxlength="200"')}${includeSummary ? `<label class="et-field et-field-wide"><span>Кратко о вашей задаче</span><textarea name="summary" aria-label="Кратко о вашей задаче" maxlength="2000" required>${escape(value.summary)}</textarea></label>` : ''}</div>${value.phone && value.email ? `<p class="et-hint et-secondary-contact">Также сохранён email: ${escape(value.email)}. Он сохранится при изменении телефона. Если ввести email, он заменит телефон и прежний email.</p>` : ''}${callbackFields(value)}<p class="et-hint et-email-note" hidden>Сохраним email для ответа специалиста. Отправка письма пока не подключена.</p>`;
  }
  function validForm(form) {
    const contact = form.elements.contact;
    const summary = form.elements.summary;
    const value = contact.value.trim();
    const digits = value.replace(/\D/g,'').length;
    const errors = [];
    if (value.includes('@') ? !validEmail(value) : (!/^\+?[0-9 ()-]+$/.test(value) || digits < 10 || digits > 15 || value.length > 40)) {
      errors.push([contact, 'Укажите телефон из 10–15 цифр или email, например name@example.ru.']);
    }
    if (summary && !summary.value.trim()) {
      errors.push([summary, 'Кратко опишите, с чем нужна помощь.']);
    }
    if(!value.includes('@') && form.elements.callback_mode?.value==='scheduled') {
      const date=form.elements.callback_date,time=form.elements.callback_time;
      const at=date.value && time.value ? `${date.value}T${time.value}:00+03:00` : '';
      const previous=current?.application;
      const unchanged=previous?.callback_mode==='scheduled' && Date.parse(previous.callback_at)===Date.parse(at);
      if(!date.value)errors.push([date,'Выберите дату звонка.']);
      if(!time.value)errors.push([time,'Выберите время звонка по Москве.']);
      if(at && !unchanged && (!Number.isFinite(Date.parse(at)) || Date.parse(at)<=Date.now()))errors.push([date,'Выберите дату и время в будущем.']);
    }
    for (const [field, message] of errors) {
      clearFieldError(field);
      const note = document.createElement('span');
      note.id = `${form.className}-${field.name}-error`;
      note.className = field === contact ? 'et-field-error et-phone-error' : 'et-field-error';
      note.textContent = message;
      field.setAttribute('aria-invalid','true');
      field.setAttribute('aria-describedby',note.id);
      field.parentElement.classList.add('et-invalid');
      field.after(note);
    }
    if (errors.length) {
      const field = errors[0][0];
      const details = field.closest('details');
      if (details) details.open = true;
      field.focus();
    }
    return errors.length === 0;
  }
  function clearFieldError(field) {
    field.removeAttribute('aria-invalid');
    field.removeAttribute('aria-describedby');
    field.parentElement.classList.remove('et-invalid');
    field.parentElement.querySelector('.et-field-error')?.remove();
  }
  function bindForm(form, save) {
    form.noValidate = true;
    syncContactMode(form);
    form.elements.contact.addEventListener('input',()=>syncContactMode(form));
    form.addEventListener('change',e=>{
      if(e.target.name!=='callback_mode')return;
      const scheduled=e.target.value==='scheduled';
      form.querySelector('.et-callback-date').hidden=!scheduled;
      form.querySelector('.et-callback-note').textContent=scheduled?'Время московское (UTC+3). Сохраним пожелание; запись на звонок пока не подтверждена.':'Укажем, что вы готовы к звонку сейчас. Немедленное соединение с менеджером не гарантируется.';
      for(const field of [form.elements.callback_date,form.elements.callback_time])clearFieldError(field);
      if(scheduled)form.elements.callback_date.focus();
    });
    form.addEventListener('input',e=>{if(e.target.hasAttribute('aria-invalid'))clearFieldError(e.target);});
    form.onsubmit = e=>{e.preventDefault();if(busy||!validForm(form))return;save(Object.fromEntries(new FormData(form)));};
  }
  function renderContacts() {
    const manualSummary = draft.summary_source !== 'conversation';
    flow.innerHTML = (current.messages.length ? previous() : '') + `<section class="et-card et-contact-card"><h2>Как с вами связаться?</h2><p class="et-hint">Достаточно телефона или email. Имя и компания — по желанию. Заявка появится в очереди; мгновенное соединение с оператором не гарантируется.</p><form class="et-contact-form">${contactFields(draft,manualSummary)}${!manualSummary && draft.summary?'<div class="et-attachment"><span>Описание задачи из нашей беседы</span><button type="button" class="et-text-button" data-action="preview-summary">Посмотреть</button></div>':''}<div class="et-stack"><button type="submit" class="et-primary">Проверить заявку</button>${button('Вернуться к консультации','continue')}${button('Продолжить без контакта','decline')}</div></form></section>`;
    const form=$('.et-contact-form');
    const saveDraft = event=>{draft={...draft,...Object.fromEntries(new FormData(form))};if(event.target.name==='summary')draft.summary_source='manual';persistForm();};
    form.addEventListener('input',saveDraft);
    form.addEventListener('change',saveDraft);
    bindForm(form,fields=>{draft=contactValues(fields,draft);noPhone=false;stage='review';render();scroll.scrollTop=0;});
  }
  function reviewHTML(application) {
    return `${application.id ? `<p class="et-eyebrow">НОМЕР ЗАЯВКИ</p><p class="et-application-number">${escape(application.number || application.id)}</p>` : ''}<p class="et-eyebrow">КОНТАКТЫ</p>${[application.full_name,application.phone,application.email,application.company].filter(Boolean).map(v=>`<p>${escape(v)}</p>`).join('')}<p class="et-eyebrow">ВРЕМЯ СВЯЗИ</p><p>${escape(application.phone ? callbackLabel(application) : 'Ответ по электронной почте')}</p><p class="et-hint">${application.phone ? 'Пожелание сохранено в заявке. Звонок пока не подтверждён.' : 'Email сохранён в заявке. Отправка письма пока не подключена.'}</p><p class="et-eyebrow">РЕЗЮМЕ</p><p class="et-application-summary">${escape(application.summary)}</p>`;
  }
  function renderReview(application,saved) {
    flow.innerHTML = (current.messages.length ? previous() : '') + assistant(saved ? 'Заявка сохранена. Её можно скачать или открыть в истории диалогов.' : 'Проверьте контакты и описание задачи. Всё верно?') + card(saved ? 'Заявка сформирована' : 'Проверьте заявку',`<p class="${saved?'et-success':'et-muted'}">${saved?'Сохранено в диалоге':'Заявка сохранится в этом диалоге'}</p>${reviewHTML(application)}`,saved ? button('Скачать заявку','download',true)+button('Продолжить консультацию','continue')+button('История диалогов','history') : button('Изменить данные','edit')+button('Сформировать заявку','save',true)+button('Вернуться к консультации','continue'),'et-review');
  }
  async function saveApplication({ full_name = '', phone = '', email = '', company = '', summary, callback_mode = 'asap', callback_date, callback_time, callback_at }) {
    const session = await api('/sessions/' + current.id + '/application', {
      full_name: full_name.trim(), phone: phone.trim(), ...(email.trim() ? {email:email.trim()} : {}), company: company.trim(), summary: summary.trim(),
      callback_mode, callback_at: callback_mode==='scheduled' ? (callback_date && callback_time ? `${callback_date}T${callback_time}:00+03:00` : callback_at || '') : '',
    });
    if (session?.id !== current.id || !session.application?.saved_at) {
      throw new Error('Сервис не подтвердил сохранение заявки. Введённые данные остались в форме.');
    }
    adopt(session);
  }
  function editContacts() {
    const saved = stage==='saved';
    const value = saved ? current.application : draft;
    openModal('Изменить контакты',`<p>Данные сохранятся только после нажатия «Сохранить».</p><form class="et-edit-form">${contactFields(value,false)}<details class="et-summary-edit"><summary>Изменить описание задачи</summary><label class="et-field"><span>Кратко о вашей задаче</span><textarea name="summary" aria-label="Кратко о вашей задаче" maxlength="2000" required>${escape(value.summary)}</textarea></label></details><div class="et-stack"><button type="submit" class="et-primary">Сохранить</button>${button('Отмена','dismiss')}</div></form>`,'edit');
    bindForm(modalBody.querySelector('form'),fields=>{
      fields = contactValues(fields,value);
      if(saved)run(async()=>{await saveApplication(fields);dismiss();status('');render();});
      else {draft={...draft,...fields,summary_source:fields.summary !== value.summary ? 'manual' : value.summary_source || 'manual'};dismiss();render();}
    });
  }
  function openModal(title,html,kind='') {
    if (!modal.open) modalFocus = document.activeElement;
    $('#et-modal-title').textContent = title;
    modalBody.innerHTML = html;
    modal.className = 'et-modal ' + kind;
    if (!modal.open) modal.showModal();
    else $('.et-modal-head button').focus();
    modal.scrollTop = 0; controls();
  }
  function dismiss() {
    if (!modal.open) return;
    modal.close();
    if (modalFocus?.isConnected) modalFocus.focus();
  }
  modal.addEventListener('cancel',e=>{e.preventDefault();dismiss();});
  modal.addEventListener('click',e=> { if (e.target === modal) { const r=modal.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)dismiss(); } });
  function menu() {
    openModal('Действия чата',`<div class="et-stack">${button('Новый диалог','new')}${button('История диалогов','history')}${button(debug ? 'Выключить режим отладки' : 'Включить режим отладки','debug')}${button('Закрыть чат','close')}${current?.application ? button('Сохранённая заявка','application')+button('Изменить контакты','edit-saved')+button('Скачать заявку','download') : button('Подготовить заявку','contacts')}${button('Вернуться на сайт','site')}</div>`,'menu');
  }
  async function history() {
    const {sessions} = await api('/sessions');
    const entries = sessions.filter(s => knownIDs.has(s.id) && (s.messages.length || s.application || hasLocalDraft(s.id))).sort((a,b)=>b.updated_at.localeCompare(a.updated_at));
    openModal('История диалогов',`<p class="et-hint">Выберите диалог, чтобы посмотреть переписку или продолжить.</p>${entries.length ? entries.map(s=>`<section class="et-card"><h2>${escape(sessionTitle(s).slice(0,100))}</h2><p class="et-hint">${escape(new Date(s.updated_at).toLocaleString('ru-RU',{timeZone:'Europe/Moscow',dateStyle:'medium',timeStyle:'short'}))}</p><p class="${s.application ? 'et-success':'et-muted'}">${s.application ? `Заявка № ${escape(s.application.number || s.application.id || '—')}`:!s.messages.length?'Черновик · ещё не отправлен':'Без заявки'}</p><button data-session="${escape(s.id)}">${!s.messages.length&&!s.application?'Открыть черновик':s.application?'Открыть диалог':'Посмотреть переписку'}</button></section>`).join('') : '<section class="et-empty-history"><h2>Здесь пока нет диалогов</h2><p>Сохранённые разговоры появятся в истории после консультации.</p></section>'}`,'drawer');
  }
  function sessionTitle(session) {
    const sent = session.messages.find(m=>m.role==='user')?.content || session.application?.summary;
    if (sent) return sent;
    try {
      const form = JSON.parse(sessionStorage.getItem(formKey(session.id)) || 'null');
      return sessionStorage.getItem(draftKey(session.id)) || form?.draft?.summary || 'Новая консультация';
    } catch (_) { return 'Новая консультация'; }
  }
  function transcriptHTML(session, context = 'current') {
    return session.messages.map((m,index)=>isRetriedQuestion(m,index,session) ? '' : `<article class="et-transcript-message"><p class="et-hint">${m.role==='user'?'Клиент':'EnergyTool'}</p><p>${escape(m.content)}</p>${debug && m.role === 'assistant' && metadata(m,index,session) ? `<button class="et-debug-link" data-source-message="${index}" data-source-context="${context}">Источники и время этого ответа</button>` : ''}</article>`).join('') || '<p>Переписка пока пуста.</p>';
  }
  async function archive(id) {
    archived=await api('/sessions/'+id);
    renderArchive();
  }
  function renderArchive() {
    if (!archived) return;
    const title=sessionTitle(archived);
    openModal('Сохранённый диалог',`${button('Назад к истории','history')}<h2 class="et-archive-title">${escape(title)}</h2><div class="et-transcript">${transcriptHTML(archived,'archive')}</div>${archived.application ? card('Заявка сформирована','<p class="et-success">Сохранено в диалоге</p>'+reviewHTML(archived.application),'','et-review') : '<p class="et-muted">Без заявки</p>'}<div class="et-stack">${button('Продолжить этот диалог','resume',true)}${button('Начать новый диалог','new')}</div>`,'archive');
  }
  function hasLocalDraft(id) {
    try {
      const form = JSON.parse(sessionStorage.getItem(formKey(id)) || 'null');
      return Boolean(sessionStorage.getItem(draftKey(id)) || sessionStorage.getItem(requestKey(id)) || form?.draft);
    } catch (_) { return false; }
  }
  async function resumeSession() {
    const target = modal.open && archived ? archived : current;
    if (!target) return;
    persistComposer(); persistForm();
    const session = await api('/sessions/'+target.id+(target.status === 'closed' ? '/resume' : ''),target.status === 'closed' ? null : undefined);
    adopt(session); stage='chat'; collapsedAt=0; status(''); dismiss(); render(); focusComposer();
  }
  function sources(result = selectedSource || current?.last_result, context = selectedSourceContext) {
    selectedSource=result;
    selectedSourceContext=context;
    const trace = result?.debug || {}, citations = citedSources(result);
    const retrieved = Array.isArray(trace.retrieved_sources) ? [...new Set(trace.retrieved_sources.filter(source => typeof source === 'string'))] : null;
    const details = `<section class="et-card"><p class="et-hint">Статус ответа</p><p>${responseLabels.get(result?.status) || 'Статус неизвестен'}</p><p class="et-hint">Время ответа</p><p class="et-source-time">${responseTime(result)}</p><p class="et-hint">Время запроса к сервису консультации, измеренное сервером. Сеть браузера не включена.</p>${Number.isFinite(trace.ml_elapsed_ms) && trace.ml_elapsed_ms >= 0 ? `<p class="et-hint">Обработка внутри ML-сервиса: ${responseTime({elapsed_ms:trace.ml_elapsed_ms})}</p>` : ''}${trace.error_code ? `<p class="et-hint">Код сбоя: <code>${escape(trace.error_code)}</code></p>` : ''}${Number.isInteger(trace.repairs) && trace.repairs > 0 ? `<p class="et-hint">Повторов после проверки формата и цитат: ${trace.repairs}</p>` : ''}</section>`;
    const documents = citations.length
      ? `<h2 class="et-source-count">Фрагменты, выбранные для ответа</h2><p class="et-hint et-source-note">Текст цитат проверен по базе знаний. Это не означает проверку каждого утверждения ответа.</p><div class="et-stack et-source-list">${citations.map((source,index)=>`<button data-fragment="${index}">${escape(source.document_id || source.source_id || 'Источник')}${source.chunk_id ? `<span class="et-hint">Раздел: ${escape(source.chunk_id)}</span>` : ''}</button>`).join('')}</div>`
      : '<h2 class="et-source-count">Для этого ответа цитаты не указаны</h2>';
    const retrieval = retrieved === null ? '<p class="et-hint et-source-note">Данные о поисковом контексте этого ответа не сохранены.</p>' : `<details class="et-retrieved"><summary>Фрагментов в контексте: ${retrieved.length}</summary><p class="et-hint">Найденный контекст не равен использованным цитатам.</p>${retrieved.length ? `<ul>${retrieved.map(source => `<li>${escape(source)}</li>`).join('')}</ul>` : '<p class="et-hint">Поиск не передал фрагменты базы.</p>'}</details>`;
    openModal('Источники и время',(context === 'archive' ? button('Назад к диалогу','archive-view') : '')+details+documents+retrieval,'drawer sources');
  }
  function fragment(index) {
    const source=citedSources(selectedSource)[index];if(!source)return;
    openModal(source.document_id || source.source_id || 'Фрагмент документа',`${button('Назад к источникам','sources')}<p class="et-muted et-source-note">Фрагмент базы знаний</p>${source.source_id ? `<p class="et-hint et-source-id">${escape(source.source_id)}</p>` : ''}<section class="et-card"><p class="et-source-quote">${escape(source.quote||'Текст цитаты не сохранён.')}</p></section><p class="et-hint et-source-note">Выбран моделью для этого ответа; текст проверен по базе знаний.</p>`,'drawer sources');
  }
  function confirm(kind) {
    const isNew = kind === 'new';
    if (!draft && !input.value.trim() && !current?.messages.length && !current?.application && !isClosed()) {
      if (!isNew) return run(closeChat);
      if (!current) return run(newSession);
      dismiss(); input.focus(); return;
    }
    const hasDraft = (draft && stage !== 'saved') || input.value.trim();
    openModal(isNew ? 'Начать новый диалог?' : 'Закрыть чат?',`<p>${isNew ? 'Текущий разговор сохранится в истории. Его можно продолжить позже. Новый диалог начнётся с чистого контекста.' : 'Разговор сохранится. При следующем открытии вы сможете продолжить с этого места.'}</p>${hasDraft ? '<p class="et-muted">Набранный вопрос и данные формы останутся в этом браузере.</p>' : ''}<div class="et-stack">${button(isNew ? 'Начать новый' : 'Закрыть чат',isNew ? 'confirm-new' : 'confirm-close',true)}${button('Вернуться в чат','dismiss')}</div>`);
  }
  async function closeChat() {
    persistComposer(); persistForm();
    location.href = returnPath;
  }
  async function newSession() {
    persistComposer(); persistForm();
    const fresh = await api('/sessions',null);
    unconfirmedMessage=null;adopt(fresh);draft=null;noPhone=false;collapsedAt=0;stage='chat';setInput('');status('');render();scroll.scrollTop=0;
  }
  async function refresh() {
    persistForm();
    if (current) adopt(await api('/sessions/'+current.id));
    else if (savedID) {
      try { adopt(await api('/sessions/'+savedID)); }
      catch(e) { if (e.code !== 'not_found') throw e;remember(null); }
    }
    if (!current || isClosed()) {
      if (!current) await newSession();
      else render();
    } else render();
    if (!needsSync) status('');
  }
  function setInput(text) {
    input.value = text;
    input.dispatchEvent(new Event('input'));
  }
  async function send(text, { retry = false, fromComposer = false } = {}) {
    if (!text.trim() || !current || isClosed() || needsSync || busy) return;
    await run(async()=> {
      const requestID = (!retry && retryRequest?.sessionID === current.id && retryRequest.text === text && retryRequest.requestID) || crypto.randomUUID();
      retryRequest = null; persistRetry();
      unconfirmedMessage = { sessionID: current.id, offset: current.messages.length, text, fromComposer, retry, restored:false, requestID };
      persistRequest();
      if (fromComposer) setInput('');
      stage='chat';status('');pending=true;waitStarted=performance.now();
      messageController = new AbortController();
      render();
      waitInterval = setInterval(updateWaiting,1000);
      $('.et-welcome').hidden=true;scroll.scrollTop=scroll.scrollHeight;
      let posted = false;
      try {
        await api('/sessions/'+current.id+'/messages',{content:text,client_request_id:requestID},messageController.signal);
        posted = true;
        const session = await api('/sessions/'+current.id,undefined,messageController.signal);
        const pinned = scroll.scrollHeight-scroll.scrollTop-scroll.clientHeight < 100;
        const oldTop = scroll.scrollTop;
        adopt(session);pending=false;render();
        scroll.scrollTop = pinned ? Math.max(0,scroll.scrollHeight-scroll.clientHeight) : oldTop;
      } catch(e) {
        if (!posted && ['history_limit','message_too_long','empty_message'].includes(e.code)) {
          if (fromComposer) setInput([text,input.value].filter(Boolean).join('\n\n'));
          unconfirmedMessage=null;persistRequest();pending=false;render();status(e.message);return;
        }
        if (unconfirmedMessage && fromComposer && !input.value.trim()) { setInput(text); unconfirmedMessage.restored = true; }
        persistRequest();
        pending=false;needsSync=true;render();
        status(e.name === 'AbortError' ? '' : (e.name === 'TimeoutError' ? 'Время ожидания истекло.' : 'Не удалось подтвердить состояние диалога.')+' История проверяется автоматически.');
      } finally {
        clearInterval(waitInterval);waitInterval=null;messageController=null;
      }
    });
    const next = afterWait; afterWait = null;
    if (next) next();
  }
  function download() {
    const a=current?.application;if(!a)return;
    const text=['Заявка EnergyTool','Номер заявки: '+(a.number || a.id || 'Не присвоен'),'Сохранено: '+new Date(a.saved_at).toLocaleString('ru-RU',{timeZone:'Europe/Moscow'}),'Имя: '+(a.full_name||'Не указано'),...(a.phone ? ['Телефон: '+a.phone] : []),...(a.email ? ['Email: '+a.email] : []),'Компания: '+(a.company||'Не указана'),'Желаемое время связи: '+(a.phone ? callbackLabel(a) : 'Ответ по электронной почте'),a.phone ? 'Звонок пока не подтверждён.' : 'Отправка письма пока не подключена.','','Задача:',a.summary,'','Сохранено локально. Передача менеджеру не подключена.'].join('\n');
    const url=URL.createObjectURL(new Blob([text],{type:'text/plain;charset=utf-8'}));const link=document.createElement('a');link.href=url;link.download='energytool-application-'+(a.number || a.id || 'saved')+'.txt';link.click();setTimeout(()=>URL.revokeObjectURL(url),1000);
  }
  host.addEventListener('click',e=> {
    const el=e.target.closest('button');if(!el||el.disabled)return;
    if(el.dataset.taskExpand){const key=el.dataset.taskExpand;taskExpanded.has(key)?taskExpanded.delete(key):taskExpanded.add(key);renderTask();$(`[data-task-expand="${key}"]`)?.focus({preventScroll:true});return;}
    if (el.dataset.taskField) return editTask(el.dataset.taskField);
    if (el.dataset.taskSuggest) { setInput([input.value.trim(),el.dataset.taskSuggest].filter(Boolean).join('\n'));focusComposer();return; }
    if (el.dataset.session) return run(()=>archive(el.dataset.session));
    if (el.dataset.sourceMessage !== undefined) {
      const context = el.dataset.sourceContext === 'archive' ? 'archive' : 'current';
      const session = context === 'archive' ? archived : current;
      const index = Number(el.dataset.sourceMessage), message = session?.messages[index];
      if (message) return sources(metadata(message,index,session),context);
    }
    if (el.dataset.fragment !== undefined) return fragment(Number(el.dataset.fragment));
    if (el.dataset.reply) { if(stage==='declined') collapsedAt=current.messages.length; return send(el.dataset.reply); }
    if (el.dataset.prompt) {
      setInput(scenarioPrompts[el.dataset.prompt]);focusComposer();return;
    }
    switch(el.dataset.action) {
      case 'task':openTask();break;
      case 'task-all':openTask();break;
      case 'plan':openModal('Ваши следующие шаги','<div class="et-plan-content"></div>','plan drawer');renderPlan();break;
      case 'plan-source':if(modal.open)dismiss();openModal('Ответ, из которого собран план',assistant(current?.messages[currentPlan?.index]?.content || ''),'archive');break;
      case 'preview-summary':openModal('Описание задачи',`<p class="et-application-summary">${escape(draft?.summary || '')}</p>`,'archive');break;
      case 'task-add':editTask();break;
      case 'stop-wait':messageController?.abort();break;
      case 'resume-wait':recoveryExpanded=true;render();checkRecovery();break;
      case 'operator':pauseWait(()=>{if(modal.open)dismiss();startContacts();});break;
      case 'resume':run(resumeSession);break;
      case 'dismiss':dismiss();break;
      case 'menu':menu();break;
      case 'history':pauseWait(()=>run(history));break;
      case 'sources':sources();break;
      case 'archive-view':renderArchive();break;
      case 'debug':debug=!debug;dismiss();render();break;
      case 'new':pauseWait(()=>confirm('new'));break;
      case 'close':pauseWait(()=>confirm('close'));break;
      case 'site':pauseWait(()=>confirm('close'));break;
      case 'confirm-new':run(async()=>{await newSession();dismiss();}).then(()=>{if(!modal.open)input.focus();});break;
      case 'confirm-close':run(closeChat);break;
      case 'refresh':run(refresh).then(()=>input.focus());break;
      case 'contacts':pauseWait(()=>{if(modal.open)dismiss();startContacts();});break;
      case 'edit':editContacts();break;
      case 'edit-saved':stage='saved';render();editContacts();break;
      case 'decline':noPhone=true;stage='declined';render();scroll.scrollTop=0;break;
      case 'application':if(modal.open)dismiss();if(current?.application){stage='saved';render();scroll.scrollTop=0;}break;
      case 'continue':continuedAnswer=`${current?.id}:${current?.messages.length}`;stage='chat';render();focusComposer();break;
      case 'transcript':openModal('Предыдущая переписка',`<div class="et-transcript">${transcriptHTML(current)}</div>`,'archive');break;
      case 'save':run(async()=>{await saveApplication(draft);stage='saved';draft=null;status('');render();scroll.scrollTop=0;});break;
      case 'download':download();break;
      case 'retry':send(current.messages.findLast(m=>m.role==='user')?.content || input.value,{retry:true});break;
    }
  });
  host.addEventListener('change',e=>{
    if(!e.target.matches('[data-plan-step]') || !currentPlan)return;
    const index=Number(e.target.dataset.planStep),step=currentPlan.steps[index];
    if(!step)return;
    planDone[step]=e.target.checked;
    try{localStorage.setItem('energytool-plan-'+current.id,JSON.stringify(planDone));}catch(_){}
    const inModal=Boolean(e.target.closest('dialog'));
    renderPlan();
    (inModal?modalBody:$('.et-plan-sidebar')).querySelector(`[data-plan-step="${index}"]`)?.focus({preventScroll:true});
  });
  $('.et-brand').addEventListener('click',e=>{e.preventDefault();if(!busy || pending)pauseWait(()=>confirm('close'));});
  $('.et-composer').onsubmit=e=> {e.preventDefault();send(input.value.trim(),{fromComposer:true});};
  function resizeComposer() {
    if(!input.offsetWidth)return;
    input.style.height='24px';input.style.height=Math.min(input.scrollHeight,120)+'px';
  }
  input.addEventListener('input',()=>{persistComposer();resizeComposer();controls();});
  let composerWidth=0;
  new ResizeObserver(entries=>{
    const width=entries[0].contentRect.width;
    if(width && width!==composerWidth){composerWidth=width;requestAnimationFrame(resizeComposer);}
  }).observe(input);
  input.addEventListener('keydown',e=>{if(e.key==='Enter'&&!e.shiftKey&&!e.isComposing){e.preventDefault();if(!$('.et-send').disabled)$('.et-composer').requestSubmit();}});
  $('.et-wait-help').innerHTML = `<p><strong>Пока жду ответ, можно дополнить задачу</strong></p><p class="et-hint">Дополнения сохранятся в черновике. Отправьте их следующим сообщением, чтобы консультант их учёл.</p><div class="et-stack"><button type="button" data-task-suggest="На объекте изменилось: ">+ Что изменилось на объекте</button><button type="button" data-task-suggest="Показания снимаем: ">+ Как снимаете показания</button>${button('Добавить факт','task-add')}</div>`;
  input.addEventListener('focus',()=>{ if (matchMedia('(max-width:760px)').matches) requestAnimationFrame(()=>$('.et-composer').scrollIntoView({block:'nearest'})); });
  $('.et-scroll').insertAdjacentHTML('afterbegin','<div class="et-application-receipt" hidden></div>');
  renderScenarios();
  $('.et-task-mobile').insertAdjacentHTML('afterend','<button type="button" class="et-plan-toggle" data-action="plan" aria-haspopup="dialog" hidden>План действий <span aria-hidden="true">↗</span></button>');
  controls();
  run(async()=>{
    const params=new URLSearchParams(location.search);
    if(params.get('start')==='new') { await newSession();params.delete('start');window.history.replaceState(null,'',location.pathname+(params.size?'?'+params.toString():'')); }
    else await refresh();
  });
})();
