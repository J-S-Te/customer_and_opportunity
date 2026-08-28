FROM golang:1.26.4-alpine AS builder

ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./

# 在镜像编译前校验 CRM/Portal 迁移归属与 SQL 可拆分性，避免漏登记的迁移直到
# docker-local 或生产迁移容器启动时才被发现。
RUN go test ./internal/migrationplan ./cmd/local-migrate

RUN set -eu; \
    for command in \
      crm-server portal-server authz-catalog production-migrate local-migrate \
      presale-integration-mock \
      contract-transfer-worker opportunity-alert-worker opportunity-owner-notification-worker \
      portal-access-disable-worker portal-feedback-worker portal-invite-compensation-worker \
      portal-project-export-worker portal-project-worker portal-report-worker \
      presale-alert-worker presale-assignment-notification-worker presale-engineer-sync-worker \
      presale-progress-notification-worker presale-report-aggregate-worker presale-worker \
      presale-worker-rollout \
      notification-delivery-worker; do \
      CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o "/out/${command}" "./cmd/${command}"; \
    done

FROM alpine:3.21 AS runtime-base

RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app

COPY --from=builder /out/authz-catalog ./authz-catalog
COPY --from=builder /out/production-migrate ./production-migrate
COPY --from=builder /out/local-migrate ./local-migrate
COPY migrations ./migrations

FROM runtime-base AS crm-runtime

COPY --from=builder /out/crm-server ./crm-server
COPY --from=builder /out/*-worker ./

EXPOSE 8090

CMD ["./crm-server"]

FROM runtime-base AS presale-worker-runtime

COPY --from=builder /out/presale-worker ./presale-worker
COPY --from=builder /out/presale-worker-rollout ./presale-worker-rollout

EXPOSE 9093

CMD ["./presale-worker"]

FROM runtime-base AS presale-alert-worker-runtime

COPY --from=builder /out/presale-alert-worker ./presale-alert-worker

CMD ["./presale-alert-worker"]

FROM runtime-base AS presale-integration-mock-runtime

COPY --from=builder /out/presale-integration-mock ./presale-integration-mock

EXPOSE 8092

CMD ["./presale-integration-mock"]

FROM runtime-base AS portal-runtime

COPY --from=builder /out/portal-server ./portal-server
COPY --from=builder /out/*-worker ./

EXPOSE 8091

CMD ["./portal-server"]
