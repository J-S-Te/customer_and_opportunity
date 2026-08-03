FROM golang:1.26.4-alpine AS builder

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/crm-server ./cmd/crm-server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/portal-server ./cmd/portal-server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/presale-worker ./cmd/presale-worker \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/authz-catalog ./cmd/authz-catalog \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/production-migrate ./cmd/production-migrate \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/local-migrate ./cmd/local-migrate

FROM alpine:3.21 AS runtime-base

RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /out/authz-catalog ./authz-catalog
COPY --from=builder /out/production-migrate ./production-migrate
COPY --from=builder /out/local-migrate ./local-migrate
COPY migrations ./migrations

FROM runtime-base AS crm-runtime

COPY --from=builder /out/crm-server ./crm-server

EXPOSE 8090

CMD ["./crm-server"]

FROM runtime-base AS presale-worker-runtime

COPY --from=builder /out/presale-worker ./presale-worker

CMD ["./presale-worker"]

FROM runtime-base AS portal-runtime

COPY --from=builder /out/portal-server ./portal-server

EXPOSE 8091

CMD ["./portal-server"]
