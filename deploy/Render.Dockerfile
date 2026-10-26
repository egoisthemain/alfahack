FROM golang:1.25.1 AS api
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 go build -o /api .

FROM python:3.11-slim
RUN apt-get update && apt-get install -y --no-install-recommends libgomp1 && rm -rf /var/lib/apt/lists/*
WORKDIR /app/model
COPY alfa_backend_handoff/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY alfa_backend_handoff/ .
COPY --from=api /api /app/api
COPY frontend/dist /app/frontend
COPY deploy/start.py /app/start.py
ENV PYTHONUNBUFFERED=1 FRONTEND_DIR=/app/frontend MODEL_URL=http://127.0.0.1:8000 DEMO_SEED=true
EXPOSE 10000
CMD ["python", "/app/start.py"]
