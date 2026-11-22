(()=>{
 document.addEventListener('click', e => {
   const button = e.target.closest('button');
   if (button && /^(Войти|Войти в кабинет)$/.test(button.textContent.trim())) {
     e.preventDefault();
     e.stopImmediatePropagation();
     location.href = 'https://energytool.mts.ru/api/auth/login?return_uri=https%3A%2F%2Flk.energytool.mts.ru%2Fselect-lk';
     return;
   }
   const a = e.target.closest('a[href]');
   if (!a) return;
   const url = new URL(a.href, location.href);
   if (url.hostname === 'energytool.mts.ru') {
     e.preventDefault();
     location.href = url.pathname + url.search + url.hash;
   }
 }, true);
 document.addEventListener('submit',e=>{if(e.target.closest('#energytool-chat'))return;e.preventDefault();e.stopImmediatePropagation();alert('Локальная демонстрация: заявка не отправлена.');},true);
})();
