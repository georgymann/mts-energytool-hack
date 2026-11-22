"""Serve the frontend and proxy API requests to the Go backend."""
from http.server import ThreadingHTTPServer, SimpleHTTPRequestHandler
from pathlib import Path
import http.client
import json
import os
from urllib.parse import urlsplit

P = Path(__file__).resolve().parent / 'public'

# The bundled reference site requests these paths from its compiled JS and CSS.
ASSET_ROUTES = {
    '/static/': '/assets/site/static/',
    '/catalog/': '/assets/site/catalog/',
    '/fonts/': '/assets/fonts/',
}


class Handler(SimpleHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    backend_url = os.getenv('BACKEND_URL', 'http://127.0.0.1:8080')

    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(P), **kwargs)

    def setup(self):
        super().setup()
        self.connection.settimeout(10)

    def log_message(self, *_):
        pass

    def end_headers(self):
        self.send_header("Cache-Control", "no-store")
        self.send_header('Content-Security-Policy', "default-src 'self' data: blob:; script-src 'self' 'unsafe-inline' 'unsafe-eval' https://api-maps.yandex.ru https://yastatic.net; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data:; connect-src 'self' https://*.yandex.ru https://*.yandex.net; frame-src 'none'; form-action 'self'")
        super().end_headers()

    def reply(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def unavailable(self):
        self.close_connection = True
        self.reply(503, {'message': 'Локальная копия: авторизация МТС и отправка заявок не подключены.'})

    def proxy(self):
        self.close_connection = True
        if self.headers.get('Transfer-Encoding'):
            return self.reply(400, {'message': 'Неподдерживаемый формат запроса.'})
        try:
            size = int(self.headers.get('Content-Length', '0'))
        except ValueError:
            size = -1
        if not 0 <= size <= 32768:
            return self.reply(413, {'message': 'Сообщение слишком длинное.'})
        target = urlsplit(self.backend_url)
        connection = http.client.HTTPConnection(target.hostname, target.port or 80, timeout=260)
        try:
            body = self.rfile.read(size) if size else None
            headers = {'Content-Type': 'application/json'}
            for key in ('Idempotency-Key', 'X-Operator-ID', 'X-Operator-Role', 'X-Operator-Token'):
                if value := self.headers.get(key):
                    headers[key] = value
            connection.request(self.command, self.path, body=body, headers=headers)
            response = connection.getresponse()
            data = response.read()
            self.send_response(response.status)
            self.send_header('Content-Type', response.getheader('Content-Type', 'application/json; charset=utf-8'))
            self.send_header('Content-Length', str(len(data)))
            for header in ('X-Request-ID', 'Content-Disposition', 'Cache-Control'):
                if value := response.getheader(header):
                    self.send_header(header, value)
            self.end_headers()
            self.wfile.write(data)
        except (OSError, http.client.HTTPException):
            self.reply(502, {'message': 'Сервис временно недоступен. Попробуйте ещё раз.'})
        finally:
            connection.close()

    def do_POST(self):
        if urlsplit(self.path).path.startswith('/api/v1/'):
            return self.proxy()
        self.unavailable()

    def do_PATCH(self):
        path = urlsplit(self.path).path
        if path.startswith('/api/v1/applications/') and path.endswith('/card'):
            return self.proxy()
        self.unavailable()

    def do_GET(self):
        path = urlsplit(self.path).path
        if path.startswith('/api/v1/') or path in ('/health/live', '/health/ready'):
            return self.proxy()
        if path == '/api/shop':
            self.path = '/public-shop.json'
            return super().do_GET()
        if path.startswith('/api/'):
            return self.unavailable()
        for prefix, destination in ASSET_ROUTES.items():
            if path.startswith(prefix):
                self.path = destination + path[len(prefix):]
                return super().do_GET()
        candidate = P / path.lstrip('/')
        if not candidate.exists() and '.' not in Path(path).name:
            self.path = '/index.html'
        super().do_GET()


if __name__ == '__main__':
    address = (os.getenv('WEB_HOST', '127.0.0.1'), int(os.getenv('WEB_PORT', '8767')))
    with ThreadingHTTPServer(address, Handler) as server:
        print(f'Energytool: http://{address[0]}:{server.server_port}', flush=True)
        server.serve_forever()
