FROM golang:1.25.1 AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 go build -o /api .
FROM alpine:3.22
WORKDIR /app
COPY --from=build /api /app/api
COPY frontend/dist /app/frontend
ENV FRONTEND_DIR=/app/frontend
EXPOSE 8080
CMD ["/app/api"]
