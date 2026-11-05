# One image holds every RidePulse program. Each compose service runs a different
# one, so the demo downloads or builds a single image.
FROM golang:1.25-alpine AS build
ENV CGO_ENABLED=0 GOWORK=off GOFLAGS=-trimpath
COPY pkg /src/pkg
COPY services /src/services
COPY tools/e2e /src/tools/e2e
RUN for s in api-gateway pricing-service matching-service assignment-service driver-simulator dashboard; do \
      cd /src/services/$s && go build -ldflags="-s -w" -o /out/$s ./cmd/$s || exit 1; \
    done; \
    cd /src/tools/e2e && go build -ldflags="-s -w" -o /out/e2e .

FROM alpine:3.20
RUN apk add --no-cache wget
COPY --from=build /out/ /usr/local/bin/
