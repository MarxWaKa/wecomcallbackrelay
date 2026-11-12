FROM golang:1.27-alpine AS builder

WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct \
    CGO_ENABLED=0 \
    GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/wecom-relay .

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/wecom-relay /app/wecom-relay

ENV TZ=Asia/Shanghai
EXPOSE 8888

ENTRYPOINT ["/app/wecom-relay"]
