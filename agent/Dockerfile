FROM python:3.11-slim@sha256:07d4824036af339fa1b6ef769cc99d9c10a141769538b52b3b597c84f7a7b535

WORKDIR /app
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 ML_HTTP_HOST=0.0.0.0
RUN useradd --uid 10001 --create-home app && chown app:app /app
COPY --chown=app:app agent/*.py agent/requirements.txt ./
USER app
EXPOSE 8090
CMD ["python", "consultation_http.py"]
