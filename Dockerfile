FROM golang:alpine AS build
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o dns-server .

FROM alpine:edge
WORKDIR /app

COPY --from=build /app/dns-server .
RUN apk --no-cache add ca-certificates tzdata

ENTRYPOINT ["/app/dns-server"]